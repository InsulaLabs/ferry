# Objects System Architecture Documentation

## Table of Contents
1. [System Overview](#system-overview)
2. [Architecture & Layering](#architecture--layering)
3. [Three Locator Types](#three-locator-types)
4. [Object Data Structure](#object-data-structure)
5. [Schema Definition](#schema-definition)
6. [Key Patterns in K/V Store](#key-patterns-in-kv-store)
7. [Operation Sequence Diagrams](#operation-sequence-diagrams)
8. [Locator Type Comparison](#locator-type-comparison)
9. [Transaction Conflict Handling](#transaction-conflict-handling)
10. [Usage Examples](#usage-examples)
11. [Error Types](#error-types)
12. [Performance Characteristics](#performance-characteristics)

---

## System Overview

The **Objects** system is a structured data layer built on top of the **Records** system. It provides:

- **Type-safe object storage** with JSON serialization
- **Versioned data** with automatic timestamp tracking
- **Three types of locators** for flexible object referencing
- **Schema-defined categories** for locator validation
- **Transactional updates** with optimistic concurrency control
- **External record references** stored within object data

### What are Objects?

Objects extend Records by adding:
- **JSON structure**: Automatic serialization/deserialization
- **Metadata**: Version numbers, created_at, updated_at timestamps
- **Typed locators**: Schema-defined categories (email, username, tags, etc.)
- **External references**: Pointers to other records stored in the object

### Core Concepts

- **ObjectController**: Manages objects of a specific typename (e.g., "users", "posts")
- **Object**: JSON structure with data, version, timestamps, and external locators
- **ObjectSchema**: Defines allowed locator categories for this object type
- **ObjectTransaction**: Wraps record transactions with object-aware operations
- **Typename**: Logical grouping that becomes the record prefix

---

## Architecture & Layering

```
┌─────────────────────────────────────────────────────────────────┐
│                      Application Layer                          │
│   Uses: ObjectController.CreateObject(), BeginTransaction()    │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                        Objects Layer                            │
│   • Manages JSON serialization                                  │
│   • Enforces schema validation                                  │
│   • Formats locators as "category:value"                        │
│   • Handles retry logic for conflicts                           │
│   • Tracks external record references                           │
│                                                                 │
│   typename: "users" → record prefix: "objects:users"           │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                        Records Layer                            │
│   • Manages record UUIDs                                        │
│   • Handles locator-to-UUID mapping                            │
│   • Provides caching & tombstone cleanup                        │
│   • Implements optimistic concurrency (CAS)                     │
│                                                                 │
│   See: records/DIAGRAM.md for details                           │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────┐
│                     INSI Key-Value Store                        │
│   • Distributed, replicated storage                             │
│   • Atomic operations (SetNX, CompareAndSwap)                   │
│   • Prefix-based iteration                                      │
└─────────────────────────────────────────────────────────────────┘
```

### Typename to Prefix Mapping

```
ObjectController created with typename:
    "users"

Internally creates RecordController with prefix:
    "objects:users"

All keys in K/V store start with:
    objects:users:locators:...
    objects:users:records:...
    objects:users:tombstones:...
```

**Code Reference**: Line 77 in objects.go
```go
recordPrefix := fmt.Sprintf("objects:%s", typename)
```

---

## Three Locator Types

### 1. Unique Locators

**Purpose**: Identity mappings (like email addresses, usernames)

```
Characteristics:
  ✓ Uniqueness enforced across all objects of this type
  ✓ Stored as record locators in K/V store
  ✓ Pre-checked before adding to prevent conflicts
  ✓ Schema-validated against UniqueLocatorCategories
  ✓ Semantic purpose: object identity

Format:
  category:value
  Example: "email:alice@example.com"
           "username:alice123"
           "phone:+1-555-1234"

Use Cases:
  • User email addresses
  • Usernames
  • Phone numbers
  • Social security numbers
  • Any globally unique identifier
```

### 2. Bundled Locators

**Purpose**: Grouping/tagging locators (semantically different from unique identity locators)

```
Characteristics:
  ✓ Uniqueness enforced (like unique locators)
  ✓ Stored as record locators in K/V store
  ✓ Pre-checked before adding to prevent conflicts
  ✓ Schema-validated against BundledLocatorCategories
  ✓ Semantic distinction from unique locators for organizational purposes

Format:
  category:value
  Example: "tag:golang"
           "tag:distributed-systems"
           "category:premium"

Use Cases:
  • Tags (each object gets unique tag locator like "tag:golang-post-123")
  • Categories (each object in category gets unique locator)
  • Groups (each member gets unique group membership locator)
  • Labels (each labeled item gets unique label locator)

Note: Despite the name "bundled", these locators are still unique per object.
The distinction from "unique" locators is semantic - bundled locators represent
grouping/categorization rather than identity. To query multiple objects with
the same tag, use a naming convention (e.g., "tag:golang-{uuid}").
```

### 3. External Record Locators

**Purpose**: References to OTHER records (foreign keys)

```
Characteristics:
  ✓ Stored INSIDE the object's JSON data
  ✓ NOT stored as record locators
  ✓ Cannot be queried via GetRecordByLocator
  ✓ Schema-validated against ExternalRecordLocatorCategories

Storage:
  Stored in Object.ExternalLocators map[string]string
  Example: {
    "user_profile": "550e8400-e29b-41d4-a716-446655440000",
    "user_settings": "7c9e6679-7425-40de-944b-e07fc1f90ae7"
  }

Use Cases:
  • User → Profile relationship
  • User → Settings relationship
  • Post → Author relationship
  • Order → Customer relationship
```

### Visual Comparison

```
Object: User "alice"
UUID: a1b2c3d4-...

Unique Locators (identity):
    email:alice@example.com ────┐
    username:alice123 ──────────┼──→ Record UUID: a1b2c3d4
    phone:+1-555-0001 ──────────┘    (stored as record locators)

Bundled Locators (grouping/tags):
    tag:premium-a1b2c3d4 ───────┐
    tag:verified-a1b2c3d4 ──────┼──→ Record UUID: a1b2c3d4
    category:admin-a1b2c3d4 ────┘    (stored as record locators)
                                     (also unique, but semantic grouping)

External Locators (references):
    {
      "profile": "uuid-of-profile-record",  ← Stored IN the
      "settings": "uuid-of-settings-record"    JSON data
    }
```

---

## Object Data Structure

### Object Struct (Go)

```go
type Object struct {
    Data             []byte            `json:"data"`
    Version          int               `json:"version"`
    CreatedAt        time.Time         `json:"created_at"`
    UpdatedAt        time.Time         `json:"updated_at"`
    ExternalLocators map[string]string `json:"external_locators,omitempty"`
}
```

**Code Reference**: Lines 34-40 in objects.go

### JSON Representation

When stored in the record's data field, the Object is marshaled to JSON:

```json
{
  "data": "dXNlciBhcHBsaWNhdGlvbiBkYXRh",
  "version": 1,
  "created_at": "2024-01-15T10:30:00Z",
  "updated_at": "2024-01-15T14:45:00Z",
  "external_locators": {
    "user_profile": "550e8400-e29b-41d4-a716-446655440000",
    "user_settings": "7c9e6679-7425-40de-944b-e07fc1f90ae7",
    "user_preferences": "9f4e7b8a-2c1d-4a3e-8f6b-1a2b3c4d5e6f"
  }
}
```

### Field Descriptions

```
data ([]byte):
    • Arbitrary application data
    • Can be anything: JSON, binary, protobuf, etc.
    • Application is responsible for encoding/decoding

version (int):
    • Always starts at 1 on creation
    • Currently not auto-incremented (application-managed)
    • Reserved for future versioning features

created_at (time.Time):
    • Set once when object is created
    • Never changes

updated_at (time.Time):
    • Set on creation
    • Updated whenever data or external_locators change
    • NOT updated when only adding/removing locators

external_locators (map[string]string):
    • Optional field (omitempty)
    • Keys: category names from schema
    • Values: UUID strings of other records
    • Managed via SetExternalRecordLocator/RemoveExternalRecordLocator
```

---

## Schema Definition

### ObjectSchema Struct

```go
type ObjectSchema struct {
    UniqueLocatorCategories         []string
    BundledLocatorCategories        []string
    ExternalRecordLocatorCategories []string
}
```

**Code Reference**: Lines 28-32 in objects.go

### Example Schema

```go
schema := ObjectSchema{
    UniqueLocatorCategories: []string{
        "email",
        "username", 
        "phone",
        "ssn",
    },
    BundledLocatorCategories: []string{
        "tag",
        "category",
        "group",
        "label",
    },
    ExternalRecordLocatorCategories: []string{
        "user_profile",
        "user_settings",
        "user_preferences",
        "primary_address",
    },
}
```

### Schema Validation

All operations validate the category against the schema:

```
AddUniqueLocator("email", "alice@example.com")
  → Checks: "email" in UniqueLocatorCategories?
  → If not: returns ErrCategoryNotInSchema

AddBundledLocator("email", "value")
  → Checks: "email" in BundledLocatorCategories?
  → If not: returns ErrCategoryNotInSchema (wrong type!)

SetExternalRecordLocator("user_profile", "uuid-123")
  → Checks: "user_profile" in ExternalRecordLocatorCategories?
  → If not: returns ErrCategoryNotInSchema
```

**Code Reference**: Lines 97-116 in objects.go (isCategoryInSchema)

---

## Key Patterns in K/V Store

### Concrete Example

```
Given:
  Typename: "users"
  Object UUID: 550e8400-e29b-41d4-a716-446655440000
  Primary locator: "user-alice-001"
  Unique locators: "email:alice@example.com", "username:alice123"
  Bundled locators: "tag:premium-550e8400", "tag:verified-550e8400"
  Object data: {
    "data": "...",
    "version": 1,
    "created_at": "2024-01-15T10:30:00Z",
    "updated_at": "2024-01-15T10:30:00Z",
    "external_locators": {
      "user_profile": "7c9e6679-7425-40de-944b-e07fc1f90ae7"
    }
  }
```

### Keys Created in INSI K/V Store

```
┌────────────────────────────────────────────────────────────────┐
│ Forward Locator Mappings (Locator → UUID)                     │
├────────────────────────────────────────────────────────────────┤
│ objects:users:locators:user-alice-001                          │
│     → "550e8400-e29b-41d4-a716-446655440000"                   │
│                                                                │
│ objects:users:locators:email:alice@example.com                 │
│     → "550e8400-e29b-41d4-a716-446655440000"                   │
│                                                                │
│ objects:users:locators:username:alice123                       │
│     → "550e8400-e29b-41d4-a716-446655440000"                   │
│                                                                │
│ objects:users:locators:tag:premium-550e8400                    │
│     → "550e8400-e29b-41d4-a716-446655440000"                   │
│                                                                │
│ objects:users:locators:tag:verified-550e8400                   │
│     → "550e8400-e29b-41d4-a716-446655440000"                   │
└────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────┐
│ Record Data (UUID → Object JSON)                              │
├────────────────────────────────────────────────────────────────┤
│ objects:users:records:550e8400-...:data                        │
│     → {                                                        │
│         "data": "...",                                         │
│         "version": 1,                                          │
│         "created_at": "2024-01-15T10:30:00Z",                  │
│         "updated_at": "2024-01-15T10:30:00Z",                  │
│         "external_locators": {                                 │
│           "user_profile": "7c9e6679-7425-40de-944b-..."        │
│         }                                                      │
│       }                                                        │
└────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────┐
│ Reverse Locator Mappings (UUID → Locators, for cleanup)       │
├────────────────────────────────────────────────────────────────┤
│ objects:users:records:550e8400-...:locator:user-alice-001      │
│     → ""                                                       │
│                                                                │
│ objects:users:records:550e8400-...:locator:email:alice@exa...  │
│     → ""                                                       │
│                                                                │
│ objects:users:records:550e8400-...:locator:username:alice123   │
│     → ""                                                       │
│                                                                │
│ objects:users:records:550e8400-...:locator:tag:premium-550e... │
│     → ""                                                       │
│                                                                │
│ objects:users:records:550e8400-...:locator:tag:verified-550... │
│     → ""                                                       │
└────────────────────────────────────────────────────────────────┘
```

### Important Notes

1. **All locators** (unique and bundled) are stored as record locators
2. **External locators** are NOT stored as separate keys; they're in the JSON
3. **Same UUID** for all locator mappings (many-to-one)
4. **Prefix** is `objects:{typename}`, not just `{typename}`
5. **Bundled locators are unique** - despite the name, they enforce uniqueness just like unique locators. The distinction is semantic (identity vs grouping).

---

## Operation Sequence Diagrams

### 1. CreateObject

```
Client          ObjectController    RecordController    INSI K/V Store
  │                    │                    │                    │
  │  CreateObject      │                    │                    │
  │  ("user-001",      │                    │                    │
  │   []byte("data"))  │                    │                    │
  ├───────────────────>│                    │                    │
  │                    │                    │                    │
  │                    │ CreateNewRecord    │                    │
  │                    │  WithLocator       │                    │
  │                    ├───────────────────>│                    │
  │                    │  "user-001"        │                    │
  │                    │                    │  SetNX (locator)   │
  │                    │                    ├───────────────────>│
  │                    │                    │  SetNX (reverse)   │
  │                    │                    ├───────────────────>│
  │                    │<───────────────────┤                    │
  │                    │  ActiveRecord      │                    │
  │                    │                    │                    │
  │                    │ [Create Object]    │                    │
  │                    │  version = 1       │                    │
  │                    │  created_at = now  │                    │
  │                    │  updated_at = now  │                    │
  │                    │  data = input      │                    │
  │                    │                    │                    │
  │                    │ [Marshal to JSON]  │                    │
  │                    │                    │                    │
  │                    │ BeginTransaction   │                    │
  │                    ├───────────────────>│                    │
  │                    │<───────────────────┤                    │
  │                    │  RecordTxn         │                    │
  │                    │                    │                    │
  │                    │ SetData(jsonBytes) │                    │
  │                    ├───────────────────>│                    │
  │                    │                    │                    │
  │                    │ Commit()           │                    │
  │                    ├───────────────────>│                    │
  │                    │                    │  SetNX (data)      │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │<───────────────────┤                    │
  │                    │  OK                │                    │
  │                    │                    │                    │
  │<───────────────────┤                    │                    │
  │  *Object           │                    │                    │
  │                    │                    │                    │

Result: Object created with UUID, data stored as JSON in record
```

**Code Reference**: Lines 338-374 in objects.go

### 2. GetObjectByLocator

```
Client          ObjectController    RecordController    INSI K/V Store
  │                    │                    │                    │
  │  GetObjectByLoc    │                    │                    │
  │  ("email:alice@")  │                    │                    │
  ├───────────────────>│                    │                    │
  │                    │                    │                    │
  │                    │ GetRecordByLocator │                    │
  │                    ├───────────────────>│                    │
  │                    │  "email:alice@..." │                    │
  │                    │                    │  Get (locator key) │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │                    │  UUID              │
  │                    │                    │                    │
  │                    │                    │  Get (tombstone)   │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │                    │  NotFound (OK)     │
  │                    │<───────────────────┤                    │
  │                    │  ActiveRecord      │                    │
  │                    │                    │                    │
  │                    │ GetData(false)     │                    │
  │                    ├───────────────────>│                    │
  │                    │                    │  Get (data key)    │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │                    │  JSON bytes        │
  │                    │<───────────────────┤                    │
  │                    │  []byte (JSON)     │                    │
  │                    │                    │                    │
  │                    │ [Unmarshal JSON]   │                    │
  │                    │  → Object struct   │                    │
  │                    │                    │                    │
  │<───────────────────┤                    │                    │
  │  *Object           │                    │                    │
  │                    │                    │                    │

Result: Object retrieved and deserialized from JSON
```

**Code Reference**: Lines 376-400 in objects.go

### 3. BeginTransaction

```
Client          ObjectController    RecordController    INSI K/V Store
  │                    │                    │                    │
  │  BeginTransaction  │                    │                    │
  │  ("user-001")      │                    │                    │
  ├───────────────────>│                    │                    │
  │                    │                    │                    │
  │                    │ GetRecordByLocator │                    │
  │                    ├───────────────────>│                    │
  │                    │<───────────────────┤                    │
  │                    │  ActiveRecord      │                    │
  │                    │                    │                    │
  │                    │ BeginTransaction   │                    │
  │                    ├───────────────────>│                    │
  │                    │                    │                    │
  │                    │                    │ [Check tombstone]  │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │                    │  NotFound (OK)     │
  │                    │                    │                    │
  │                    │                    │ [Force reload data]│
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │                    │  JSON bytes        │
  │                    │                    │                    │
  │                    │<───────────────────┤                    │
  │                    │  RecordTxn         │                    │
  │                    │  (snapshot data)   │                    │
  │                    │                    │                    │
  │                    │ [Unmarshal JSON]   │                    │
  │                    │  → Object struct   │                    │
  │                    │                    │                    │
  │                    │ [Create ObjectTxn] │                    │
  │                    │  wraps RecordTxn   │                    │
  │                    │  snapshotObject    │                    │
  │                    │  empty change maps │                    │
  │                    │                    │                    │
  │<───────────────────┤                    │                    │
  │  ObjectTransaction │                    │                    │
  │                    │                    │                    │

Result: Transaction created with fresh snapshot
```

**Code Reference**: Lines 297-336 in objects.go

### 4. Transaction: Multi-Operation Commit

```
Client          ObjectTxn           RecordTxn           INSI K/V Store
  │                  │                    │                    │
  │  UpdateData      │                    │                    │
  ├─────────────────>│                    │                    │
  │                  │ [Store in memory]  │                    │
  │                  │  pendingData = new │                    │
  │                  │                    │                    │
  │  AddUniqueLoc    │                    │                    │
  │  ("email", "a@") │                    │                    │
  ├─────────────────>│                    │                    │
  │                  │ [Validate schema]  │                    │
  │                  │ [Format: "email:a@"]                    │
  │                  │  uniqueToAdd[...] │                    │
  │                  │                    │                    │
  │  AddBundledLoc   │                    │                    │
  │  ("tag", "go")   │                    │                    │
  ├─────────────────>│                    │                    │
  │                  │ [Format: "tag:go"] │                    │
  │                  │  bundledToAdd[...] │                    │
  │                  │                    │                    │
  │  SetExternalLoc  │                    │                    │
  │  ("profile","id")│                    │                    │
  ├─────────────────>│                    │                    │
  │                  │ [Store in ops[]]   │                    │
  │                  │                    │                    │
  │     Commit       │                    │                    │
  ├─────────────────>│                    │                    │
  │                  │                    │                    │
  │                  │ [Update Object]    │                    │
  │                  │  obj.Data = new    │                    │
  │                  │  obj.UpdatedAt=now │                    │
  │                  │  obj.ExtLocs[...]=id                    │
  │                  │                    │                    │
  │                  │ [Marshal Object]   │                    │
  │                  │  → JSON bytes      │                    │
  │                  │                    │                    │
  │                  │ SetData(jsonBytes) │                    │
  │                  ├───────────────────>│                    │
  │                  │                    │                    │
  │                  │ AddLocator         │                    │
  │                  │  ("email:a@...")   │                    │
  │                  ├───────────────────>│                    │
  │                  │                    │                    │
  │                  │ AddLocator         │                    │
  │                  │  ("tag:go")        │                    │
  │                  ├───────────────────>│                    │
  │                  │                    │                    │
  │                  │ Commit()           │                    │
  │                  ├───────────────────>│                    │
  │                  │                    │                    │
  │                  │                    │  CAS (data)        │
  │                  │                    ├───────────────────>│
  │                  │                    │  SetNX (email:a@)  │
  │                  │                    ├───────────────────>│
  │                  │                    │  Set (reverse)     │
  │                  │                    ├───────────────────>│
  │                  │                    │  SetNX (tag:go)    │
  │                  │                    ├───────────────────>│
  │                  │                    │  Set (reverse)     │
  │                  │                    ├───────────────────>│
  │                  │                    │<───────────────────┤
  │                  │<───────────────────┤  All OK            │
  │<─────────────────┤  OK                │                    │
  │                  │                    │                    │

Result: Object data updated, new locators added, all atomic
```

**Code Reference**: Lines 220-275 in objects.go (objectTransactionImpl.Commit)

### 5. AddUniqueLocator (Convenience Method with Retry)

```
Client          ObjectController    RecordController    INSI K/V Store
  │                    │                    │                    │
  │  AddUniqueLocator  │                    │                    │
  │  ("user-001",      │                    │                    │
  │   "email",         │                    │                    │
  │   "new@mail.com")  │                    │                    │
  ├───────────────────>│                    │                    │
  │                    │                    │                    │
  │                    │ [Validate schema]  │                    │
  │                    │  "email" in        │                    │
  │                    │  UniqueCategories? │                    │
  │                    │                    │                    │
  │                    │ [Pre-check exists] │                    │
  │                    │ GetRecordByLocator │                    │
  │                    │  ("email:new@...")  │                   │
  │                    ├───────────────────>│                    │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │<───────────────────┤  NotFound (OK)     │
  │                    │                    │                    │
  │                    │ [Retry loop: 3x]   │                    │
  │                    │                    │                    │
  │                    │ BeginTransaction   │                    │
  │                    ├───────────────────>│                    │
  │                    │<───────────────────┤                    │
  │                    │  ObjectTxn         │                    │
  │                    │                    │                    │
  │                    │ txn.AddUniqueLocator                    │
  │                    │  ("email", "new@") │                    │
  │                    │                    │                    │
  │                    │ txn.Commit()       │                    │
  │                    ├───────────────────>│                    │
  │                    │                    ├───────────────────>│
  │                    │                    │<───────────────────┤
  │                    │<───────────────────┤  OK or Conflict    │
  │                    │                    │                    │
  │                    │ [If conflict]      │                    │
  │                    │  sleep + retry     │                    │
  │                    │                    │                    │
  │<───────────────────┤                    │                    │
  │  OK (or error)     │                    │                    │
  │                    │                    │                    │

Result: New unique locator added with automatic retry on conflict
```

**Code Reference**: Lines 452-492 in objects.go

### 6. DeleteObject

```
Client          ObjectController    RecordController    INSI K/V Store
  │                    │                    │                    │
  │  DeleteObject      │                    │                    │
  │  ("user-001")      │                    │                    │
  ├───────────────────>│                    │                    │
  │                    │                    │                    │
  │                    │ GetRecordByLocator │                    │
  │                    ├───────────────────>│                    │
  │                    │<───────────────────┤                    │
  │                    │  ActiveRecord      │                    │
  │                    │                    │                    │
  │                    │ DeleteRecord(UUID) │                    │
  │                    ├───────────────────>│                    │
  │                    │                    │                    │
  │                    │                    │  Set (tombstone)   │
  │                    │                    ├───────────────────>│
  │                    │                    │  timestamp         │
  │                    │                    │<───────────────────┤
  │                    │<───────────────────┤  OK                │
  │                    │  OK                │                    │
  │                    │                    │                    │
  │<───────────────────┤                    │                    │
  │  OK                │                    │                    │
  │                    │                    │                    │
  │                    │                    │ [Later: cleanup]   │
  │                    │                    │  Delete all locs   │
  │                    │                    │  Delete data       │
  │                    │                    │  Delete tombstone  │
  │                    │                    │                    │

Phase 1: Tombstone marks object deleted (immediate)
Phase 2: Background worker cleans up all keys (async)

Note: ALL locators (unique + bundled) removed by records cleanup
      External locators simply disappear with the JSON data
```

**Code Reference**: Lines 431-450 in objects.go

---

## Locator Type Comparison

```
┌──────────────────┬─────────────┬──────────────┬───────────────────┐
│ Feature          │ Unique      │ Bundled      │ External          │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Uniqueness       │ Enforced    │ Enforced     │ N/A               │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Stored as record │ Yes         │ Yes          │ No                │
│ locator          │             │              │                   │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Stored in JSON   │ No          │ No           │ Yes (in map)      │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Queryable via    │ Yes         │ Yes          │ No                │
│ GetRecordByLoc   │             │              │                   │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ K/V storage      │ 2 keys/loc  │ 2 keys/loc   │ 0 keys            │
│ overhead         │ (fwd + rev) │ (fwd + rev)  │ (just JSON bytes) │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Conflict check   │ Pre-checked │ Pre-checked  │ Not applicable    │
│ before add       │             │              │                   │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Semantic purpose │ Identity    │ Grouping/Tag │ N/A               │
│                  │             │              │                   │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Cleanup on       │ Auto        │ Auto         │ Auto (with JSON)  │
│ delete           │ (records)   │ (records)    │                   │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Schema category  │ Unique      │ Bundled      │ External          │
│ array            │ LocCats     │ LocCats      │ RecLocCats        │
├──────────────────┼─────────────┼──────────────┼───────────────────┤
│ Use cases        │ Identity    │ Grouping     │ Foreign keys      │
│                  │ Email       │ Tags         │ Relations         │
│                  │ Username    │ Categories   │ References        │
│                  │ Phone       │ Labels       │                   │
└──────────────────┴─────────────┴──────────────┴───────────────────┘
```

### Code Examples

```go
txn, _ := controller.BeginTransaction("user-001")

txn.AddUniqueLocator("email", "alice@example.com")
txn.AddBundledLocator("tag", "premium-user-001")
txn.SetExternalRecordLocator("profile", "uuid-of-profile-record")

err := txn.Commit()
```

Result in K/V store:
```
Forward locators created:
  objects:users:locators:email:alice@example.com → UUID
  objects:users:locators:tag:premium-user-001 → UUID

Reverse locators created:
  objects:users:records:UUID:locator:email:alice@example.com → ""
  objects:users:records:UUID:locator:tag:premium-user-001 → ""

JSON data updated:
  objects:users:records:UUID:data → {
    ...,
    "external_locators": {
      "profile": "uuid-of-profile-record"
    }
  }
```

---

## Transaction Conflict Handling

### Inherited from Records Layer

Objects inherit optimistic concurrency control from the records system:

- **Snapshot Isolation**: Each transaction reads a consistent snapshot
- **Compare-And-Swap (CAS)**: Atomic check-and-update on commit
- **Conflict Detection**: If data changed between snapshot and commit, conflict occurs

### Automatic Retry Logic

All mutating convenience methods implement retry logic:

```
Methods with automatic retry:
  • UpdateObjectData          (max 3 retries)
  • AddUniqueLocator          (max 3 retries)
  • RemoveUniqueLocator       (max 3 retries)
  • UpdateUniqueLocator       (max 3 retries)
  • AddBundledLocator         (max 3 retries)
  • RemoveBundledLocator      (max 3 retries)
  • SetExternalRecordLocator  (max 3 retries)
  • RemoveExternalRecordLocator (max 3 retries)
```

### Retry Pattern

```go
maxRetries := 3
for attempt := 0; attempt < maxRetries; attempt++ {
    txn, err := oc.BeginTransaction(locator)
    if err != nil {
        return err
    }

    err = txn.Commit()
    if err == nil {
        return nil
    }

    if errors.Is(err, records.ErrTransactionConflict) {
        oc.logger.Warn("transaction conflict, retrying", "attempt", attempt+1)
        continue
    }

    return err
}

return fmt.Errorf("failed after %d retries due to conflicts", maxRetries)
```

**Code References**: 
- Lines 402-429 (UpdateObjectData)
- Lines 452-492 (AddUniqueLocator)
- Lines 494-527 (RemoveUniqueLocator)

### Manual Transaction (No Retry)

When using BeginTransaction directly, application handles retry:

```go
const maxRetries = 5

for attempt := 0; attempt < maxRetries; attempt++ {
    txn, err := controller.BeginTransaction("user-001")
    if err != nil {
        return err
    }

    txn.UpdateData(newData)
    txn.AddUniqueLocator("email", "new@example.com")

    err = txn.Commit()
    if err == nil {
        break
    }

    if errors.Is(err, records.ErrTransactionConflict) {
        time.Sleep(time.Millisecond * time.Duration(rand.Intn(100)))
        continue
    }

    return err
}
```

### Conflict Scenarios

```
Scenario 1: Concurrent Data Updates
  Client A: Begin → UpdateData("v2") → Commit ✓
  Client B: Begin → UpdateData("v3") → Commit ✗ (conflict)
  Result: Client B must retry with fresh snapshot

Scenario 2: Concurrent Locator Adds
  Client A: Begin → AddUniqueLocator("email", "a@") → Commit ✓
  Client B: Begin → AddUniqueLocator("phone", "+1") → Commit ✗ (conflict)
  Result: Client B must retry (even though different locators)
  Reason: Both modify the record's locator list

Scenario 3: Pre-checked Unique Locator
  Client A: AddUniqueLocator("email", "a@") [pre-checks, begins txn]
  Client B: AddUniqueLocator("email", "a@") [pre-checks, sees conflict]
  Result: Client B fails immediately with ErrLocatorAlreadyExists
  Benefit: Avoids wasted transaction attempt
```

---

## Usage Examples

### Example 1: Creating a User Object

```go
ctx := context.Background()
insiClient := getInsiClient()
logger := slog.Default()

schema := ObjectSchema{
    UniqueLocatorCategories: []string{"email", "username"},
    BundledLocatorCategories: []string{"tag"},
    ExternalRecordLocatorCategories: []string{"profile"},
}

controller := NewObjectController(
    ctx,
    "users",
    schema,
    5*time.Second,
    60*time.Second,
    30*time.Second,
    logger,
    insiClient,
)
controller.Start()
defer controller.Stop()

userData := []byte(`{"name":"Alice","age":30}`)
obj, err := controller.CreateObject("user-alice-001", userData)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Created object version %d at %v\n", obj.Version, obj.CreatedAt)
```

**Test Reference**: Lines 108-135 in objects_test.go

### Example 2: Adding Multiple Locators

```go
primaryLocator := "user-alice-001"

err := controller.AddUniqueLocator(primaryLocator, "email", "alice@example.com")
if err != nil {
    log.Fatal(err)
}

err = controller.AddUniqueLocator(primaryLocator, "username", "alice123")
if err != nil {
    log.Fatal(err)
}

err = controller.AddBundledLocator(primaryLocator, "tag", "premium")
if err != nil {
    log.Fatal(err)
}

err = controller.AddBundledLocator(primaryLocator, "tag", "verified")
if err != nil {
    log.Fatal(err)
}
```

Now retrievable via any locator:
```go
obj1, _ := controller.GetObjectByLocator("user-alice-001")
obj2, _ := controller.GetObjectByLocator("email:alice@example.com")
obj3, _ := controller.GetObjectByLocator("username:alice123")
obj4, _ := controller.GetObjectByLocator("tag:premium-user-alice-001")
obj5, _ := controller.GetObjectByLocator("tag:verified-user-alice-001")
```

**Test Reference**: Lines 330-359 in objects_test.go

### Example 3: Multi-Operation Transaction

```go
txn, err := controller.BeginTransaction("user-alice-001")
if err != nil {
    log.Fatal(err)
}

snapshot := txn.GetSnapshot()
fmt.Printf("Current version: %d\n", snapshot.Version)

txn.UpdateData([]byte(`{"name":"Alice","age":31}`))

txn.AddUniqueLocator("email", "alice@example.com")
txn.AddBundledLocator("tag", "premium-user-alice-001")
txn.SetExternalRecordLocator("profile", "550e8400-e29b-41d4-a716-446655440000")

err = txn.Commit()
if err != nil {
    if errors.Is(err, records.ErrTransactionConflict) {
        fmt.Println("Conflict detected, retry needed")
    }
    log.Fatal(err)
}

fmt.Println("All changes committed atomically")
```

**Test Reference**: Lines 265-328 in objects_test.go

### Example 4: External Locator Management

```go
primaryLocator := "user-alice-001"

err := controller.SetExternalRecordLocator(
    primaryLocator,
    "profile",
    "550e8400-e29b-41d4-a716-446655440000",
)
if err != nil {
    log.Fatal(err)
}

err = controller.SetExternalRecordLocator(
    primaryLocator,
    "settings",
    "7c9e6679-7425-40de-944b-e07fc1f90ae7",
)
if err != nil {
    log.Fatal(err)
}

profileUUID, err := controller.GetExternalRecordLocator(primaryLocator, "profile")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("User's profile UUID: %s\n", profileUUID)

err = controller.RemoveExternalRecordLocator(primaryLocator, "settings")
if err != nil {
    log.Fatal(err)
}
```

**Test Reference**: Lines 529-613 in objects_test.go

### Example 5: Updating a Unique Locator

```go
primaryLocator := "user-alice-001"

err := controller.AddUniqueLocator(primaryLocator, "email", "old@example.com")

err = controller.UpdateUniqueLocator(
    primaryLocator,
    "email",
    "old@example.com",
    "new@example.com",
)
if err != nil {
    log.Fatal(err)
}

_, err = controller.GetObjectByLocator("email:old@example.com")
if err != ErrObjectNotFound {
    log.Fatal("Old email should not work")
}

obj, err := controller.GetObjectByLocator("email:new@example.com")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Successfully updated email locator\n")
```

**Test Reference**: Lines 394-425 in objects_test.go

### Example 6: Iterating Objects

```go
offset := 0
limit := 100

for {
    objects, err := controller.IterateObjects(offset, limit)
    if err != nil {
        log.Fatal(err)
    }

    if len(objects) == 0 {
        break
    }

    for _, obj := range objects {
        fmt.Printf("Object: version=%d, created=%v\n", 
            obj.Version, obj.CreatedAt)
        
        if obj.ExternalLocators != nil {
            for category, uuid := range obj.ExternalLocators {
                fmt.Printf("  %s → %s\n", category, uuid)
            }
        }
    }

    offset += limit
}
```

**Test Reference**: Lines 740-769 in objects_test.go

### Example 7: Deleting an Object

```go
err := controller.DeleteObject("user-alice-001")
if err != nil {
    log.Fatal(err)
}

time.Sleep(time.Second)

_, err = controller.GetObjectByLocator("user-alice-001")
if err != ErrObjectNotFound {
    log.Fatal("Object should be deleted")
}

_, err = controller.GetObjectByLocator("email:alice@example.com")
if err != ErrObjectNotFound {
    log.Fatal("All locators should be deleted")
}

fmt.Println("Object and all locators cleaned up")
```

**Test Reference**: Lines 680-700 in objects_test.go

---

## Error Types

### Package-Level Errors

```go
var (
    ErrObjectNotFound        = errors.New("object not found")
    ErrLocatorAlreadyExists  = errors.New("locator already exists")
    ErrLocatorNotFound       = errors.New("locator not found")
    ErrInvalidCategory       = errors.New("invalid category")
    ErrCategoryNotInSchema   = errors.New("category not in schema")
    ErrFailedToLoadObject    = errors.New("failed to load object")
    ErrFailedToUpdateObject  = errors.New("failed to update object")
    ErrFailedToDeleteObject  = errors.New("failed to delete object")
    ErrFailedToMarshalObject = errors.New("failed to marshal object")
)
```

**Code Reference**: Lines 16-26 in objects.go

### Error Descriptions

```
ErrObjectNotFound:
    When: GetObjectByLocator, BeginTransaction, DeleteObject
    Cause: Locator doesn't exist or record is tombstoned
    Action: Check locator spelling, verify object wasn't deleted

ErrLocatorAlreadyExists:
    When: CreateObject, AddUniqueLocator, UpdateUniqueLocator
    Cause: Trying to create/add a locator that already exists
    Action: Choose different locator or retrieve existing object

ErrLocatorNotFound:
    When: GetExternalRecordLocator
    Cause: External locator category not set in object
    Action: Set the external locator first or handle missing case

ErrInvalidCategory:
    When: Internal validation (not currently used)
    Cause: Reserved for future use
    Action: N/A

ErrCategoryNotInSchema:
    When: Any locator operation with invalid category
    Cause: Category not in the controller's schema definition
    Action: Check schema, ensure category was added to correct array

ErrFailedToLoadObject:
    When: GetObjectByLocator, BeginTransaction
    Cause: Underlying record error, JSON unmarshal failure
    Action: Check logs for underlying error details

ErrFailedToUpdateObject:
    When: UpdateObjectData, SetExternalRecordLocator
    Cause: Transaction failed after max retries
    Action: Check for high contention, review conflict handling

ErrFailedToDeleteObject:
    When: DeleteObject
    Cause: Underlying record deletion failed
    Action: Check logs for underlying error details

ErrFailedToMarshalObject:
    When: Internal (not currently returned directly)
    Cause: JSON marshal failure during commit
    Action: Check object data is valid
```

### Wrapped Errors from Records Layer

Objects can also return wrapped records errors:

```
records.ErrTransactionConflict:
    When: Manual transaction commit
    Cause: Concurrent modification detected
    Action: Retry transaction with fresh snapshot

records.ErrRecordDeleted:
    When: Any operation on deleted object
    Cause: Object was deleted (tombstone exists)
    Action: Object is being cleaned up, wait and check

records.ErrNewRecordLocatorTooShort:
records.ErrNewRecordLocatorTooLong:
    When: Any locator operation
    Cause: Locator length validation failed
    Action: Adjust locator length (4-256 chars)
```

---

## Performance Characteristics

### Time Complexity

```
Operation                      Time Complexity    Network Calls
──────────────────────────────────────────────────────────────────
CreateObject                   O(1)               3 (create record + txn + commit)
GetObjectByLocator             O(1)               2 (get UUID + get data)
UpdateObjectData               O(1)               3-9 (begin + commit, up to 3 retries)
BeginTransaction               O(1)               3 (get record + tombstone + data)
Transaction.Commit:
  - UpdateData only            O(1)               1 (CAS on data)
  - Add N unique locators      O(N)               2N (forward + reverse each)
  - Add N bundled locators     O(N)               2N (forward + reverse each)
  - Set M external locators    O(M)               0 (in JSON, no extra calls)
DeleteObject                   O(1)               2 (lookup + tombstone)
Cleanup deleted object         O(L)               2L+2 (L locators + data + tombstone)
IterateObjects                 O(N)               N (get data for each)
AddUniqueLocator               O(1)               5-15 (precheck + txn, up to 3 retries)
GetExternalRecordLocator       O(1)               2 (get record + get data)

Where:
  N = number of objects
  L = number of locators per object
  M = number of external locators
```

### Storage Overhead

```
Per Object:
  Base (no locators):
    • 1 data key (JSON)
    • Minimal: ~200 bytes (empty object structure)

  Per Unique Locator:
    • 1 forward key (locator → UUID)
    • 1 reverse key (UUID → locator marker)
    • Total: ~100-300 bytes per locator

  Per Bundled Locator:
    • 1 forward key (locator → UUID)
    • 1 reverse key (UUID → locator marker)
    • Total: ~100-300 bytes per locator

  Per External Locator:
    • 0 separate keys
    • ~50-100 bytes in JSON
    • Much cheaper than unique/bundled!

Example User Object:
  • 1 primary locator: "user-alice-001"
  • 2 unique locators: "email:...", "username:..."
  • 3 bundled locators: "tag:premium-uuid", "tag:verified-uuid", "category:admin-uuid"
  • 2 external locators: "profile", "settings"

  Total Keys: 1 data + 6*2 locator keys = 13 keys
  Estimated Size: ~2KB (including JSON data)
  
  Note: Bundled locators must be unique like all other locators.
```

### Optimization Strategies

```
Use External Locators for References:
  ✓ No K/V overhead (just JSON bytes)
  ✓ Faster commits (no additional locator operations)
  ✗ Cannot query by external locator

Batch Multiple Changes in One Transaction:
  ✓ Single CAS operation for data
  ✓ All locator changes in one commit
  ✓ Atomic consistency

Limit Unique/Bundled Locators:
  • Each locator = 2 K/V keys
  • Consider if you really need that locator
  • Use external locators for references

Cache Frequently Accessed Objects:
  • Records layer provides automatic caching
  • Default: 5 seconds (configurable)
  • Reduces network calls for hot objects

Pre-check Before Add:
  • AddUniqueLocator pre-checks existence
  • Prevents wasted transaction attempts
  • Useful for preventing conflicts
```

### Concurrency Performance

```
Optimistic Locking Benefits:
  ✓ No distributed locks needed
  ✓ High read concurrency (no blocking)
  ✓ Fast path for low-contention workloads

Conflict Scenarios:
  Low contention (< 1% conflict rate):
    • Excellent performance
    • Most operations succeed first try

  Medium contention (1-10% conflict rate):
    • Good performance
    • Automatic retry handles most conflicts
    • Average 1-2 attempts per operation

  High contention (> 10% conflict rate):
    • Degraded performance
    • Many retries needed
    • Consider redesign (split hot objects)
```

---

## Best Practices

### Schema Design

```
✓ Define comprehensive schema upfront
✓ Group related locators in appropriate category
✓ Use unique locators for identity (email, username)
✓ Use bundled locators for tags and categories
✓ Use external locators for relationships
✗ Don't change schema after deployment (no migration support)
✗ Don't mix unique and bundled semantics
```

### Locator Usage

```
✓ Keep locator values human-readable when possible
✓ Use consistent formatting (lowercase emails, etc.)
✓ Consider locator length (4-256 chars enforced)
✓ Pre-check unique locators before adding
✓ Make bundled locators unique per object (e.g., "tag:golang-{uuid}")
✓ Both unique and bundled locators are pre-checked and must be unique
✗ Don't use locators for temporary flags
✗ Don't create locators for every possible query
✗ Don't assume bundled locators can be shared between objects
```

### Transaction Patterns

```
✓ Group related changes in single transaction
✓ Keep transactions short-lived
✓ Use convenience methods for single operations
✓ Implement retry logic for manual transactions
✗ Don't hold transactions open for user input
✗ Don't nest transactions
```

### Error Handling

```
✓ Check for ErrTransactionConflict and retry
✓ Check for ErrLocatorAlreadyExists before add
✓ Log underlying errors for debugging
✓ Handle ErrCategoryNotInSchema (schema mismatch)
✗ Don't ignore errors silently
✗ Don't retry non-conflict errors
```

---

End of Documentation
