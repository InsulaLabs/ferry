# Record System Architecture Documentation

## Table of Contents
1. [System Overview](#system-overview)
2. [Key Structure & Data Model](#key-structure--data-model)
3. [Locator-to-Record Mapping](#locator-to-record-mapping)
4. [Operation Sequence Diagrams](#operation-sequence-diagrams)
5. [Cache Behavior](#cache-behavior)
6. [Concurrency Control](#concurrency-control)

---

## System Overview

The record system provides a managed abstraction over a distributed key-value store (INSI). It enables:
- Storage of arbitrary data chunks (records) identified by UUIDs
- Multiple human-readable locators pointing to the same record (many-to-one)
- Transactional updates with optimistic concurrency control
- Automatic cleanup of deleted records via tombstones

**Core Concepts:**
- **Record**: A data blob with a unique UUID
- **Locator**: A human-readable key that references a record UUID
- **Transaction**: ACID-compliant operation with snapshot isolation
- **Tombstone**: Deletion marker for background cleanup

---

## Key Structure & Data Model

### Key Patterns in K/V Store

```
┌─────────────────────────────────────────────────────────────────────┐
│                        INSI Key-Value Store                         │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  [1] LOCATOR KEYS (Forward Mapping)                                │
│      Pattern: {prefix}:locators:{LOCATOR}                          │
│      Value:   {RECORD_UUID}                                        │
│                                                                     │
│      Example:                                                       │
│      app:locators:user@email.com  →  a1b2c3d4-uuid                 │
│      app:locators:username123     →  a1b2c3d4-uuid                 │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  [2] RECORD DATA KEYS                                              │
│      Pattern: {prefix}:records:{RECORD_UUID}:data                  │
│      Value:   {ACTUAL_DATA_BLOB}                                   │
│                                                                     │
│      Example:                                                       │
│      app:records:a1b2c3d4-uuid:data  →  {"name":"John","age":30}   │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  [3] RECORD LOCATOR KEYS (Reverse Mapping)                         │
│      Pattern: {prefix}:records:{RECORD_UUID}:locator:{LOCATOR}     │
│      Value:   "" (empty, used for iteration/cleanup)               │
│                                                                     │
│      Example:                                                       │
│      app:records:a1b2c3d4-uuid:locator:user@email.com  →  ""       │
│      app:records:a1b2c3d4-uuid:locator:username123     →  ""       │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  [4] TOMBSTONE KEYS (Deletion Markers)                             │
│      Pattern: {prefix}:tombstones:{RECORD_UUID}                    │
│      Value:   {UNIX_TIMESTAMP}                                     │
│                                                                     │
│      Example:                                                       │
│      app:tombstones:a1b2c3d4-uuid  →  1699876543                   │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

### Why Bidirectional Mapping?

```
Forward Mapping (Locator → UUID):
    Fast lookup by locator
    Used for: GetRecordByLocator()

Reverse Mapping (UUID → Locators):
    Enables cleanup when record deleted
    Used for: Iterating all locators of a record
    Used for: DeleteRecord() cleanup process
```

---

## Locator-to-Record Mapping

### Single Record, Multiple Locators

```
                    ┌──────────────────────────┐
                    │   Record UUID:           │
                    │   a1b2c3d4-5678-uuid     │
                    │                          │
                    │   Data:                  │
                    │   {"name": "Alice",      │
                    │    "role": "admin"}      │
                    └──────────────────────────┘
                             ▲
                             │
            ┌────────────────┼────────────────┐
            │                │                │
            │                │                │
    ┌───────┴──────┐  ┌──────┴──────┐  ┌─────┴──────┐
    │  Locator 1   │  │  Locator 2  │  │ Locator 3  │
    │              │  │             │  │            │
    │ alice@co.com │  │  alice123   │  │  admin-01  │
    └──────────────┘  └─────────────┘  └────────────┘
```

### Concrete Example in K/V Store

```
Given:
    Prefix: "app"
    Record UUID: "550e8400-e29b-41d4-a716-446655440000"
    Locators: "alice@example.com", "alice_admin"
    Data: {"user":"alice","admin":true}

Keys Created:
┌────────────────────────────────────────────────────────────────────┐
│ Forward Locator Mappings                                           │
├────────────────────────────────────────────────────────────────────┤
│ app:locators:alice@example.com                                     │
│     → "550e8400-e29b-41d4-a716-446655440000"                       │
│                                                                    │
│ app:locators:alice_admin                                           │
│     → "550e8400-e29b-41d4-a716-446655440000"                       │
└────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────┐
│ Record Data                                                        │
├────────────────────────────────────────────────────────────────────┤
│ app:records:550e8400-e29b-41d4-a716-446655440000:data              │
│     → {"user":"alice","admin":true}                                │
└────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────┐
│ Reverse Locator Mappings                                           │
├────────────────────────────────────────────────────────────────────┤
│ app:records:550e8400-e29b-41d4-a716-446655440000:locator:alice@... │
│     → ""                                                           │
│                                                                    │
│ app:records:550e8400-e29b-41d4-a716-446655440000:locator:alice_... │
│     → ""                                                           │
└────────────────────────────────────────────────────────────────────┘
```

---

## Operation Sequence Diagrams

### 1. CreateNewRecordWithLocator

```
Client          RecordController        INSI K/V Store
  │                    │                      │
  │  CreateNewRecord   │                      │
  │   "alice@co.com"   │                      │
  ├───────────────────>│                      │
  │                    │                      │
  │                    │ [Validate Locator]   │
  │                    │  - Length check      │
  │                    │  - 4-256 chars       │
  │                    │                      │
  │                    │ [Generate UUID]      │
  │                    │  uuid.New()          │
  │                    │                      │
  │                    │  SetNX (locator key) │
  │                    ├─────────────────────>│
  │                    │  Key: prefix:locators:alice@co.com
  │                    │  Val: {UUID}         │
  │                    │                      │
  │                    │<─────────────────────┤
  │                    │  OK / ErrConflict    │
  │                    │                      │
  │                    │ [If conflict]        │
  │                    │  Return ErrNotUnique │
  │                    │                      │
  │                    │  Set (reverse key)   │
  │                    ├─────────────────────>│
  │                    │  Key: prefix:records:{UUID}:locator:alice@co.com
  │                    │  Val: ""             │
  │                    │                      │
  │                    │<─────────────────────┤
  │                    │  OK                  │
  │                    │                      │
  │<───────────────────┤                      │
  │  ActiveRecord obj  │                      │
  │  UUID: {UUID}      │                      │
  │                    │                      │

Result: Record created with UUID, locator mapped bidirectionally
```

### 2. GetRecordByLocator

```
Client          RecordController        INSI K/V Store
  │                    │                      │
  │  GetRecordByLoc    │                      │
  │   "alice@co.com"   │                      │
  ├───────────────────>│                      │
  │                    │                      │
  │                    │ [Validate Locator]   │
  │                    │                      │
  │                    │  Get (locator key)   │
  │                    ├─────────────────────>│
  │                    │  Key: prefix:locators:alice@co.com
  │                    │                      │
  │                    │<─────────────────────┤
  │                    │  UUID or NotFound    │
  │                    │                      │
  │                    │ [If NotFound]        │
  │                    │  Return ErrRecordNF  │
  │                    │                      │
  │                    │  Get (tombstone)     │
  │                    ├─────────────────────>│
  │                    │  Key: prefix:tombstones:{UUID}
  │                    │                      │
  │                    │<─────────────────────┤
  │                    │  Exists / NotFound   │
  │                    │                      │
  │                    │ [If exists]          │
  │                    │  Return ErrDeleted   │
  │                    │                      │
  │                    │ [Create ActiveRecord]│
  │                    │  - Store UUID        │
  │                    │  - Empty cache       │
  │                    │  - Zero timestamps   │
  │                    │                      │
  │<───────────────────┤                      │
  │  ActiveRecord obj  │                      │
  │                    │                      │

Result: ActiveRecord handle returned (lazy-loads data)
```

### 3. Transaction: SetData (Initial)

```
Client          ActiveRecord      Transaction         INSI K/V Store
  │                  │                  │                    │
  │ BeginTransaction │                  │                    │
  ├─────────────────>│                  │                    │
  │                  │                  │                    │
  │                  │ [Check tombstone]│                    │
  │                  ├─────────────────────────────────────> │
  │                  │                  │  Get tombstone    │
  │                  │<─────────────────────────────────────┤
  │                  │                  │  NotFound (OK)    │
  │                  │                  │                    │
  │                  │ [Fetch current data for snapshot]    │
  │                  ├─────────────────────────────────────> │
  │                  │                  │  Get data key     │
  │                  │<─────────────────────────────────────┤
  │                  │                  │  "" (no data yet) │
  │                  │                  │                    │
  │                  │ [Create Transaction]                 │
  │                  │  - snapshotData: nil                 │
  │                  │  - snapshotLocators: [...]           │
  │                  │                  │                    │
  │<─────────────────┤                  │                    │
  │  Transaction obj │                  │                    │
  │                  │                  │                    │
  │    SetData       │                  │                    │
  │  (new payload)   │                  │                    │
  ├──────────────────┼─────────────────>│                    │
  │                  │                  │ [Store in memory]  │
  │                  │                  │  newData = payload │
  │                  │                  │                    │
  │     Commit       │                  │                    │
  ├──────────────────┼─────────────────>│                    │
  │                  │                  │                    │
  │                  │                  │ [Check tombstone]  │
  │                  │                  ├───────────────────>│
  │                  │                  │<───────────────────┤
  │                  │                  │  OK                │
  │                  │                  │                    │
  │                  │                  │ [snapshotData==nil]│
  │                  │                  │  Use SetNX         │
  │                  │                  │                    │
  │                  │                  │  SetNX (data key)  │
  │                  │                  ├───────────────────>│
  │                  │                  │  Key: prefix:records:{UUID}:data
  │                  │                  │  Val: {payload}    │
  │                  │                  │                    │
  │                  │                  │<───────────────────┤
  │                  │                  │  OK / ErrConflict  │
  │                  │                  │                    │
  │                  │                  │ [If conflict]      │
  │                  │                  │  Return ErrTxnConf │
  │                  │                  │                    │
  │                  │                  │ [Update cache]     │
  │                  │                  │  record.data=new   │
  │                  │                  │  timestamp=now     │
  │<──────────────────────────────────────                   │
  │       OK         │                  │                    │
  │                  │                  │                    │

Result: Data written with SetNX (create if not exists)
```

### 4. Transaction: SetData (Update with CAS)

```
Client          ActiveRecord      Transaction         INSI K/V Store
  │                  │                  │                    │
  │ BeginTransaction │                  │                    │
  ├─────────────────>│                  │                    │
  │                  │                  │                    │
  │                  │ [Fetch current data]                 │
  │                  ├─────────────────────────────────────> │
  │                  │<─────────────────────────────────────┤
  │                  │                  │  "old-value"       │
  │                  │                  │                    │
  │                  │ [Create snapshot]│                    │
  │                  │  snapshot = "old-value"              │
  │                  │                  │                    │
  │<─────────────────┤                  │                    │
  │  Transaction obj │                  │                    │
  │                  │                  │                    │
  │    SetData       │                  │                    │
  │  "new-value"     │                  │                    │
  ├──────────────────┼─────────────────>│                    │
  │                  │                  │  newData="new..."  │
  │                  │                  │                    │
  │     Commit       │                  │                    │
  ├──────────────────┼─────────────────>│                    │
  │                  │                  │                    │
  │                  │                  │ [snapshotData!=nil]│
  │                  │                  │  Use CAS           │
  │                  │                  │                    │
  │                  │                  │ CompareAndSwap     │
  │                  │                  ├───────────────────>│
  │                  │                  │  Key: data key     │
  │                  │                  │  Old: "old-value"  │
  │                  │                  │  New: "new-value"  │
  │                  │                  │                    │
  │                  │                  │<───────────────────┤
  │                  │                  │  OK / ErrConflict  │
  │                  │                  │                    │
  │<──────────────────────────────────────                   │
  │       OK         │                  │                    │
  │                  │                  │                    │

Result: Data updated with optimistic locking (CAS)
        If another txn modified data, conflict detected
```

### 5. Transaction: AddLocator

```
Client          Transaction         INSI K/V Store
  │                  │                    │
  │  AddLocator      │                    │
  │  "newalias"      │                    │
  ├─────────────────>│                    │
  │                  │ [Validate]         │
  │                  │  Length check      │
  │                  │                    │
  │                  │ [Store in memory]  │
  │                  │  locatorsToAdd[]   │
  │                  │                    │
  │     Commit       │                    │
  ├─────────────────>│                    │
  │                  │                    │
  │                  │ [For each locator in locatorsToAdd]
  │                  │                    │
  │                  │  SetNX (forward)   │
  │                  ├───────────────────>│
  │                  │  Key: prefix:locators:newalias
  │                  │  Val: {UUID}       │
  │                  │                    │
  │                  │<───────────────────┤
  │                  │  OK / Conflict     │
  │                  │                    │
  │                  │ [If conflict: warn, continue]
  │                  │                    │
  │                  │  Set (reverse)     │
  │                  ├───────────────────>│
  │                  │  Key: prefix:records:{UUID}:locator:newalias
  │                  │  Val: ""           │
  │                  │                    │
  │                  │<───────────────────┤
  │                  │  OK                │
  │                  │                    │
  │                  │ [Invalidate cache] │
  │                  │  lastLocatorUpdate=0
  │                  │                    │
  │<─────────────────┤                    │
  │       OK         │                    │
  │                  │                    │

Result: New locator points to same record
        Conflict logged but doesn't fail transaction
```

### 6. Transaction: RemoveLocator

```
Client          Transaction         INSI K/V Store
  │                  │                    │
  │  RemoveLocator   │                    │
  │  "oldalias"      │                    │
  ├─────────────────>│                    │
  │                  │ [Store in memory]  │
  │                  │  locatorsToRemove[]│
  │                  │                    │
  │     Commit       │                    │
  ├─────────────────>│                    │
  │                  │                    │
  │                  │ [For each locator in locatorsToRemove]
  │                  │                    │
  │                  │  Delete (forward)  │
  │                  ├───────────────────>│
  │                  │  Key: prefix:locators:oldalias
  │                  │                    │
  │                  │<───────────────────┤
  │                  │  OK                │
  │                  │                    │
  │                  │  Delete (reverse)  │
  │                  ├───────────────────>│
  │                  │  Key: prefix:records:{UUID}:locator:oldalias
  │                  │                    │
  │                  │<───────────────────┤
  │                  │  OK                │
  │                  │                    │
  │                  │ [Invalidate cache] │
  │                  │  lastLocatorUpdate=0
  │                  │                    │
  │<─────────────────┤                    │
  │       OK         │                    │
  │                  │                    │

Result: Locator removed from both forward and reverse mappings
```

### 7. DeleteRecord & Tombstone Cleanup

```
Client      RecordController    Background Worker    INSI K/V Store
  │                │                    │                    │
  │  DeleteRecord  │                    │                    │
  │   {UUID}       │                    │                    │
  ├───────────────>│                    │                    │
  │                │                    │                    │
  │                │  Set (tombstone)   │                    │
  │                ├────────────────────────────────────────>│
  │                │  Key: prefix:tombstones:{UUID}         │
  │                │  Val: {timestamp}  │                    │
  │                │                    │                    │
  │                │<────────────────────────────────────────┤
  │                │  OK                │                    │
  │<───────────────┤                    │                    │
  │     OK         │                    │                    │
  │                │                    │                    │
  │ [Record marked deleted, operations blocked]             │
  │                │                    │                    │
  │                │                    │ [Cleanup interval] │
  │                │                    │  +jitter fires     │
  │                │                    │                    │
  │                │                    │ IterateByPrefix    │
  │                │                    ├───────────────────>│
  │                │                    │ prefix:tombstones: │
  │                │                    │                    │
  │                │                    │<───────────────────┤
  │                │                    │ [{UUID}, ...]      │
  │                │                    │                    │
  │                │                    │ [For each tombstone]
  │                │                    │                    │
  │                │                    │ [Check tombstone]  │
  │                │                    ├───────────────────>│
  │                │                    │<───────────────────┤
  │                │                    │ Exists (proceed)   │
  │                │                    │                    │
  │                │                    │ IterateByPrefix    │
  │                │                    ├───────────────────>│
  │                │                    │ prefix:records:{UUID}:locator:
  │                │                    │                    │
  │                │                    │<───────────────────┤
  │                │                    │ [locator keys...]  │
  │                │                    │                    │
  │                │                    │ [For each locator] │
  │                │                    │                    │
  │                │                    │ Delete (forward)   │
  │                │                    ├───────────────────>│
  │                │                    │ prefix:locators:{loc}
  │                │                    │                    │
  │                │                    │ Delete (reverse)   │
  │                │                    ├───────────────────>│
  │                │                    │ prefix:records:{UUID}:locator:{loc}
  │                │                    │                    │
  │                │                    │ Delete (data)      │
  │                │                    ├───────────────────>│
  │                │                    │ prefix:records:{UUID}:data
  │                │                    │                    │
  │                │                    │ Delete (tombstone) │
  │                │                    ├───────────────────>│
  │                │                    │ prefix:tombstones:{UUID}
  │                │                    │                    │
  │                │                    │<───────────────────┤
  │                │                    │ All keys removed   │
  │                │                    │                    │

Phase 1: Tombstone created (immediate, blocks operations)
Phase 2: Background cleanup (async, removes all traces)
```

### 8. Concurrent Transaction Conflict

```
Client A        Client B         Record          INSI K/V Store
  │                │                │                    │
  │ BeginTxn       │                │                    │
  ├───────────────────────────────>│                    │
  │                │                │ [Snapshot: "v1"]   │
  │<───────────────────────────────┤                    │
  │  Txn A         │                │                    │
  │                │                │                    │
  │                │  BeginTxn      │                    │
  │                ├───────────────>│                    │
  │                │                │ [Snapshot: "v1"]   │
  │                │<───────────────┤                    │
  │                │  Txn B         │                    │
  │                │                │                    │
  │  SetData("v2") │                │                    │
  ├───────────────────────────────>│                    │
  │                │                │ [TxnA: newData=v2] │
  │                │                │                    │
  │                │  SetData("v3") │                    │
  │                ├───────────────>│                    │
  │                │                │ [TxnB: newData=v3] │
  │                │                │                    │
  │  Commit        │                │                    │
  ├───────────────────────────────>│                    │
  │                │                │  CompareAndSwap    │
  │                │                ├───────────────────>│
  │                │                │  Old: "v1"         │
  │                │                │  New: "v2"         │
  │                │                │                    │
  │                │                │<───────────────────┤
  │                │                │  SUCCESS           │
  │<───────────────────────────────┤                    │
  │  OK            │                │                    │
  │                │                │ [Data now = "v2"]  │
  │                │                │                    │
  │                │  Commit        │                    │
  │                ├───────────────>│                    │
  │                │                │  CompareAndSwap    │
  │                │                ├───────────────────>│
  │                │                │  Old: "v1"         │
  │                │                │  New: "v3"         │
  │                │                │                    │
  │                │                │<───────────────────┤
  │                │                │  CONFLICT!         │
  │                │                │ (actual is "v2")   │
  │                │                │                    │
  │                │<───────────────┤                    │
  │                │ ErrTxnConflict │                    │
  │                │                │                    │
  │                │ [Must retry]   │                    │
  │                │  Begin new txn │                    │
  │                │  (gets "v2")   │                    │
  │                │                │                    │

Result: Client B must retry transaction with fresh snapshot
        Optimistic concurrency control via CAS
```

---

## Cache Behavior

### Data Caching with TTL

```
┌─────────────────────────────────────────────────────────────┐
│                    ActiveRecord Instance                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Fields:                                                    │
│    data []byte               ← Cached data                 │
│    lastDataUpdate time.Time  ← When cached                 │
│                                                             │
│  cacheDuration (controller): 5 seconds                      │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  GetData(forceReload=false)                                 │
│                                                             │
│    ┌─────────────────────────────────────┐                 │
│    │  Cache Valid?                       │                 │
│    │  now - lastUpdate < cacheDuration   │                 │
│    └─────────────┬───────────────────────┘                 │
│                  │                                          │
│         YES ─────┴───── NO                                  │
│          │               │                                  │
│          │               │                                  │
│      ┌───▼───┐       ┌───▼──────────────┐                  │
│      │Return │       │ Fetch from INSI  │                  │
│      │Cached │       │ Update cache     │                  │
│      │ Data  │       │ Update timestamp │                  │
│      └───────┘       └──────────────────┘                  │
│                                                             │
│  GetData(forceReload=true)                                  │
│                                                             │
│    Always fetches from INSI                                 │
│    Updates cache & timestamp                                │
│    (Locator cache NOT refreshed)                            │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Cache Invalidation

```
Operation              Data Cache    Locator Cache
────────────────────────────────────────────────────
CreateNewRecord        N/A           Set (now)
GetData(false)         Check/Update  No change
GetData(true)          Force update  No change
GetLocators()          No change     Check/Update
Transaction.Commit:
  - SetData            Update cache  No change
  - AddLocator         No change     Invalidate (zero)
  - RemoveLocator      No change     Invalidate (zero)
BeginTransaction       Force reload  Check/Update (cached)
```

### Cache Flow Diagram

```
Time: 0s
   ActiveRecord created
   data: nil
   lastDataUpdate: zero
   lastLocatorUpdate: zero

Time: 1s
   GetData(false) called
   → Cache invalid (zero timestamp)
   → Fetch from INSI: "hello"
   → data = "hello"
   → lastDataUpdate = 1s

Time: 2s
   GetData(false) called
   → Cache valid (2s - 1s < 5s)
   → Return "hello" (no fetch)

Time: 7s
   GetData(false) called
   → Cache expired (7s - 1s >= 5s)
   → Fetch from INSI: "hello-modified"
   → data = "hello-modified"
   → lastDataUpdate = 7s

Time: 8s
   GetData(true) called
   → Force reload (ignore cache)
   → Fetch from INSI: "hello-again"
   → data = "hello-again"
   → lastDataUpdate = 8s
```

---

## Concurrency Control

### Optimistic Locking Strategy

```
Traditional Pessimistic Locking:
    ┌──────────────────┐
    │ LOCK record      │
    │ Read data        │
    │ Modify data      │
    │ Write data       │
    │ UNLOCK record    │
    └──────────────────┘
    
    Problem: Distributed locking is complex & slow

Optimistic Locking (Used Here):
    ┌──────────────────┐
    │ Read data (v1)   │  ← Snapshot
    │ Modify locally   │
    │ CAS: if still v1 │  ← Atomic check & write
    │   then write v2  │
    │ else CONFLICT    │  ← Retry needed
    └──────────────────┘
    
    Benefit: No locks, high concurrency, fast for low-conflict workloads
```

### Transaction Isolation

```
SNAPSHOT ISOLATION

Transaction A begins at T1:
  Snapshot = current state at T1
  
Transaction B begins at T2:
  Snapshot = current state at T2
  
Transaction A modifies record at T3
Transaction A commits at T4
  → Changes visible globally

Transaction B still sees snapshot from T2
  → Isolated from A's changes
  
Transaction B commits at T5
  → CAS detects conflict (expected old != actual current)
  → Returns ErrTransactionConflict
  → Transaction B must retry with fresh snapshot

Properties:
  ✓ Atomicity: CAS is atomic operation
  ✓ Consistency: Validation ensures constraints
  ✓ Isolation: Snapshot per transaction
  ✓ Durability: INSI persists to disk
```

### Conflict Resolution Pattern

```
func UpdateRecordWithRetry(record ActiveRecord, updateFn func([]byte) []byte) error {
    const maxRetries = 5
    
    for i := 0; i < maxRetries; i++ {
        
        txn, err := record.BeginTransaction()
        if err != nil {
            return err
        }
        
        currentData := txn.GetSnapshotData()
        newData := updateFn(currentData)
        
        txn.SetData(newData)
        
        err = txn.Commit()
        if err == nil {
            return nil  // Success
        }
        
        if err == ErrTransactionConflict {
            // Retry with fresh snapshot
            time.Sleep(time.Millisecond * time.Duration(rand.Intn(100)))
            continue
        }
        
        return err  // Other error
    }
    
    return errors.New("max retries exceeded")
}
```

---

## Error Handling & Edge Cases

### Tombstone Behavior

```
State Transition:

   ACTIVE                    TOMBSTONED                   REMOVED
     │                            │                           │
     │  DeleteRecord()            │  Background cleanup       │
     ├───────────────────────────>├──────────────────────────>│
     │                            │                           │
     │                            │                           │
Operations allowed:        Operations blocked:         No keys exist
- GetRecordByLocator       - GetRecordByLocator        - Locators freed
- BeginTransaction         - BeginTransaction          - Data deleted
- GetData                  - Any mutation              - Can reuse locators
- GetLocators                                          

Return: Normal            Return: ErrRecordDeleted    Return: ErrRecordNotFound
```

### Validation Rules

```
Locator Constraints:
  ┌────────────────────────────────────────┐
  │ Minimum Length: 4 characters           │
  │ Maximum Length: 256 characters         │
  │ Must be unique across all records      │
  │   (per prefix/namespace)               │
  └────────────────────────────────────────┘

Errors:
  ErrNewRecordLocatorTooShort   (< 4 chars)
  ErrNewRecordLocatorTooLong    (> 256 chars)
  ErrNewRecordLocatorNotUnique  (SetNX conflict)
  ErrRecordNotFound             (locator doesn't exist)
  ErrRecordDeleted              (tombstone exists)
  ErrTransactionConflict        (CAS failed)
  ErrTransactionAlreadyCommitted (double commit)
  ErrTransactionNoChanges       (empty commit)
```

### Cleanup Timing

```
Timeline of Deletion:

T=0: DeleteRecord() called
      │
      └─> Tombstone created IMMEDIATELY
          All operations blocked
          
T=0 to T=cleanup_interval:
      │
      └─> Record inaccessible but keys still in K/V store
          Attempting to use locator: ErrRecordDeleted
          
T=cleanup_interval (+jitter):
      │
      └─> Background worker processes tombstones
          All keys removed from K/V store
          
T=cleanup_interval+1:
      │
      └─> Record completely gone
          Attempting to use locator: ErrRecordNotFound
          Locators can be reused for new records

Jitter: Random 0 to cleanupJitter duration added
        Prevents thundering herd if multiple workers
```

---

## Usage Examples

### Basic Record Creation

```go
controller := NewRecordController(
    ctx,
    "myapp",              // prefix
    5*time.Second,        // cache duration
    60*time.Second,       // cleanup interval
    30*time.Second,       // cleanup jitter
    logger,
    insiClient,
)
controller.Start()
defer controller.Stop()

record, err := controller.CreateNewRecordWithLocator("user@example.com")
if err != nil {
    // Handle ErrNewRecordLocatorNotUnique, etc.
}

uuid := record.GetUniqueID()
```

### Transactional Update

```go
txn, err := record.BeginTransaction()
if err != nil {
    // Handle error
}

userData := txn.GetSnapshotData()

err = txn.AddLocator("user_alternate_email")
if err != nil {
    // Handle validation error
}

txn.SetData([]byte(`{"name":"Alice","version":2}`))

err = txn.Commit()
if err == ErrTransactionConflict {
    // Retry with fresh transaction
} else if err != nil {
    // Handle other error
}
```

### Iteration

```go
offset := 0
limit := 100

for {
    records, err := controller.IterateRecords(offset, limit)
    if err != nil {
        break
    }
    
    if len(records) == 0 {
        break
    }
    
    for _, rec := range records {
        data := rec.GetData(false)
        // Process data
    }
    
    offset += limit
}
```

---

## Performance Characteristics

```
Operation                Time Complexity    Network Calls
─────────────────────────────────────────────────────────
CreateNewRecord          O(1)               2 writes
GetRecordByLocator       O(1)               2 reads
GetData (cached)         O(1)               0
GetData (uncached)       O(1)               1 read
GetLocators (cached)     O(1)               0
GetLocators (uncached)   O(L)               1 prefix scan (L locators)
Transaction Begin        O(1)               2 reads
Transaction Commit       O(L+D)             L+D writes (locators + data)
  - SetData              O(1)               1 CAS
  - Add N locators       O(N)               2N writes
  - Remove N locators    O(N)               2N deletes
DeleteRecord             O(1)               1 write (tombstone)
Cleanup Record           O(L)               2L+2 deletes
IterateRecords           O(N)               1 prefix scan

Where:
  L = number of locators for a record
  D = data operations (0 or 1)
  N = total records in namespace
```

---

## Architecture Benefits

1. **Flexibility**: Multiple locators per record enables user-friendly references
2. **Consistency**: Bidirectional mapping ensures cleanup correctness
3. **Safety**: Tombstones prevent race conditions during deletion
4. **Performance**: Caching reduces network calls for hot data
5. **Scalability**: Optimistic locking scales better than pessimistic
6. **Simplicity**: Clean abstraction over raw K/V operations

---

End of Documentation

