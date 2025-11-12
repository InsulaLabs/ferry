package records

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/InsulaLabs/insi/client"
	"github.com/google/uuid"
)

/*
	We Map "Locator Keys" to a single UUID

	The benefit to this is that the data will then be able to be modified in a user
	repl, validated by us, and then re-stored so we dont have to transcode

	prefix:locators:<LOCATOR>  →  <RECORD_UUID>
    prefix:records:<RECORD_UUID>:data  →  <RECORD DATA>
	prefix:records:<RECORD_UUID>:locator:<LOCATOR>  →  ""

	This allows us to store any given chunk of data and reference it from an arbitrary
	and managed many->one relations (managed meaning the references are cleand up on deletion)

	This abstraction allows for easier general-use storage over the insi k/v store

	Deletion Workflow:

	When a record is deleted, it is marked with a "tombstone" in its context.
	A background process, the tombstone runner, will later remove all references
	to records marked for deletion.

	While a tombstone is present, all operations on that record are blocked.

	There is a brief delay between marking a record as deleted and its actual removal.
	Developers should keep this in mind: for example, if you delete a user's email locator
	and immediately try to create a new user with the same email before cleanup finishes,
	an error will occur.
*/

const (
	MinLocatorLength = 4
	MaxLocatorLength = 256
)

var (
	ErrNewRecordLocatorNotUnique          = errors.New("new record locator is not unique")
	ErrNewRecordLocatorTooShort           = errors.New("new record locator is too short")
	ErrNewRecordLocatorTooLong            = errors.New("new record locator is too long")
	ErrNewRecordContainsInvalidCharacters = errors.New("new record locator contains invalid characters")
	ErrRecordNotFound                     = errors.New("record not found")
	ErrRecordDeleted                      = errors.New("record has been deleted")
	ErrTransactionConflict                = errors.New("transaction conflict: data was modified concurrently")
	ErrTransactionAlreadyCommitted        = errors.New("transaction has already been committed")
	ErrTransactionNoChanges               = errors.New("transaction has no changes to commit")
)

type RecordTransaction interface {
	GetSnapshotData() []byte
	GetSnapshotLocators() []string

	SetData(data []byte)
	AddLocator(locator string) error
	RemoveLocator(locator string) error

	Commit() error
	Rollback()
}

type ActiveRecord interface {

	// Never changes. its the records unique id
	GetUniqueID() string

	// caches data, will return cache if last update not expired, unless
	// forceReload is true, then the data (and only data) will be refreshed
	// if an error occurs on a force reload or a timed pull,
	// the cached data will be handed back, and HasError() will return true, etc
	GetData(forceReload bool) []byte

	// caches internally, doesnt make a thread, but stores "last update" time
	// and if exceeded then will pull new locators set
	GetLocators() []string

	BeginTransaction() (RecordTransaction, error)

	HasError() bool
	GetError() error
	ClearError()
}

type RecordController interface {
	GetRecordGroupPrefix() string

	GetRecordByLocator(locator string) (ActiveRecord, error)

	CreateNewRecordWithLocator(locator string) (ActiveRecord, error)

	DeleteRecord(recordUUID string) error

	IterateRecords(offset, limit int) ([]ActiveRecord, error)

	Start()

	Stop()
}

func NewRecordController(
	ctx context.Context,
	prefix string,
	cacheDuration time.Duration,
	cleanupInterval time.Duration,
	cleanupJitter time.Duration,
	logger *slog.Logger,
	insiClient *client.Client,
) RecordController {

	return &recordControllerImpl{
		ctx:             ctx,
		prefix:          prefix,
		cacheDuration:   cacheDuration,
		cleanupInterval: cleanupInterval,
		cleanupJitter:   cleanupJitter,
		logger:          logger,
		insiClient:      insiClient,
		stopChan:        make(chan struct{}),
		stoppedChan:     make(chan struct{}),
	}
}

type recordControllerImpl struct {
	ctx             context.Context
	prefix          string
	cacheDuration   time.Duration
	cleanupInterval time.Duration
	cleanupJitter   time.Duration
	logger          *slog.Logger
	insiClient      *client.Client
	stopChan        chan struct{}
	stoppedChan     chan struct{}
}

func (r *recordControllerImpl) GetRecordGroupPrefix() string {
	return r.prefix
}

func (r *recordControllerImpl) validateLocator(locator string) error {
	if len(locator) < MinLocatorLength {
		return ErrNewRecordLocatorTooShort
	}
	if len(locator) > MaxLocatorLength {
		return ErrNewRecordLocatorTooLong
	}
	return nil
}

func (r *recordControllerImpl) buildLocatorKey(locator string) string {
	return fmt.Sprintf("%s:locators:%s", r.prefix, locator)
}

func (r *recordControllerImpl) buildRecordDataKey(recordUUID string) string {
	return fmt.Sprintf("%s:records:%s:data", r.prefix, recordUUID)
}

func (r *recordControllerImpl) buildRecordLocatorKey(recordUUID, locator string) string {
	return fmt.Sprintf("%s:records:%s:locator:%s", r.prefix, recordUUID, locator)
}

func (r *recordControllerImpl) buildTombstoneKey(recordUUID string) string {
	return fmt.Sprintf("%s:tombstones:%s", r.prefix, recordUUID)
}

func (r *recordControllerImpl) checkTombstone(recordUUID string) error {
	tombstoneKey := r.buildTombstoneKey(recordUUID)

	_, err := client.WithRetries(r.ctx, client.CONFIG_MAX_VOID_RETRIES, r.logger, func() (string, error) {
		return r.insiClient.Get(tombstoneKey)
	})
	if err != nil {
		if errors.Is(err, client.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("failed to check tombstone for record %s: %w", recordUUID, err)
	}
	return ErrRecordDeleted
}

func (r *recordControllerImpl) GetRecordByLocator(locator string) (ActiveRecord, error) {
	if err := r.validateLocator(locator); err != nil {
		return nil, err
	}

	locatorKey := r.buildLocatorKey(locator)

	recordUUID, err := client.WithRetries(r.ctx, client.CONFIG_MAX_VOID_RETRIES, r.logger, func() (string, error) {
		return r.insiClient.Get(locatorKey)
	})
	if err != nil {
		if errors.Is(err, client.ErrKeyNotFound) {
			return nil, ErrRecordNotFound
		}
		return nil, fmt.Errorf("failed to get record UUID for locator %s: %w", locator, err)
	}

	if err := r.checkTombstone(recordUUID); err != nil {
		return nil, err
	}

	return &activeRecordImpl{
		uniqueID:          recordUUID,
		data:              nil,
		locators:          nil,
		lastDataUpdate:    time.Time{},
		lastLocatorUpdate: time.Time{},
		controller:        r,
		err:               nil,
	}, nil
}

func (r *recordControllerImpl) CreateNewRecordWithLocator(locator string) (ActiveRecord, error) {
	if err := r.validateLocator(locator); err != nil {
		return nil, err
	}

	locatorKey := r.buildLocatorKey(locator)

	recordUUID := uuid.New().String()

	err := client.WithRetriesVoid(r.ctx, r.logger, func() error {
		return r.insiClient.SetNX(locatorKey, recordUUID)
	})
	if err != nil {
		if errors.Is(err, client.ErrConflict) {
			return nil, ErrNewRecordLocatorNotUnique
		}
		return nil, fmt.Errorf("failed to create locator mapping: %w", err)
	}

	recordLocatorKey := r.buildRecordLocatorKey(recordUUID, locator)
	err = client.WithRetriesVoid(r.ctx, r.logger, func() error {
		return r.insiClient.Set(recordLocatorKey, "")
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create reverse locator mapping: %w", err)
	}

	return &activeRecordImpl{
		uniqueID:          recordUUID,
		data:              nil,
		locators:          []string{locator},
		lastDataUpdate:    time.Time{},
		lastLocatorUpdate: time.Now(),
		controller:        r,
		err:               nil,
	}, nil
}

type activeRecordImpl struct {
	uniqueID          string
	data              []byte
	locators          []string
	lastDataUpdate    time.Time
	lastLocatorUpdate time.Time
	controller        *recordControllerImpl

	err error
}

func (r *activeRecordImpl) GetUniqueID() string {
	return r.uniqueID
}

func (r *activeRecordImpl) GetData(forceReload bool) []byte {
	now := time.Now()
	cacheValid := !r.lastDataUpdate.IsZero() && now.Sub(r.lastDataUpdate) < r.controller.cacheDuration

	if !forceReload && cacheValid && r.data != nil {
		return r.data
	}

	if err := r.controller.checkTombstone(r.uniqueID); err != nil {
		r.err = err
		if r.data != nil {
			return r.data
		}
		return nil
	}

	dataKey := r.controller.buildRecordDataKey(r.uniqueID)

	data, err := client.WithRetries(r.controller.ctx, client.CONFIG_MAX_VOID_RETRIES, r.controller.logger, func() (string, error) {
		return r.controller.insiClient.Get(dataKey)
	})
	if err != nil {
		if errors.Is(err, client.ErrKeyNotFound) {
			r.data = nil
			r.lastDataUpdate = time.Now()
			r.err = nil
			return nil
		}
		r.err = fmt.Errorf("failed to get data for record %s: %w", r.uniqueID, err)
		if r.data != nil {
			return r.data
		}
		return nil
	}

	r.data = []byte(data)
	r.lastDataUpdate = time.Now()
	r.err = nil
	return r.data
}

func (r *activeRecordImpl) GetLocators() []string {
	now := time.Now()
	cacheValid := !r.lastLocatorUpdate.IsZero() && now.Sub(r.lastLocatorUpdate) < r.controller.cacheDuration

	if cacheValid && r.locators != nil {
		return r.locators
	}

	prefix := fmt.Sprintf("%s:records:%s:locator:", r.controller.prefix, r.uniqueID)

	keys, err := client.WithRetries(r.controller.ctx, client.CONFIG_MAX_VOID_RETRIES, r.controller.logger, func() ([]string, error) {
		return r.controller.insiClient.IterateByPrefix(prefix, 0, 1000)
	})
	if err != nil {
		r.err = fmt.Errorf("failed to get locators for record %s: %w", r.uniqueID, err)
		if r.locators != nil {
			return r.locators
		}
		return []string{}
	}

	locators := make([]string, 0, len(keys))
	for _, key := range keys {
		locator := strings.TrimPrefix(key, prefix)
		locators = append(locators, locator)
	}

	r.locators = locators
	r.lastLocatorUpdate = time.Now()
	r.err = nil
	return r.locators
}

func (r *activeRecordImpl) BeginTransaction() (RecordTransaction, error) {
	if err := r.controller.checkTombstone(r.uniqueID); err != nil {
		return nil, err
	}

	snapshotData := r.GetData(true)
	if r.HasError() {
		if !errors.Is(r.GetError(), client.ErrKeyNotFound) {
			return nil, r.GetError()
		}
		r.ClearError()
		snapshotData = nil
	}

	snapshotLocators := r.GetLocators()

	return &recordTransactionImpl{
		record:           r,
		snapshotData:     snapshotData,
		snapshotLocators: snapshotLocators,
		newData:          nil,
		locatorsToAdd:    make([]string, 0),
		locatorsToRemove: make([]string, 0),
		committed:        false,
	}, nil
}

func (r *activeRecordImpl) HasError() bool {
	return r.err != nil
}

func (r *activeRecordImpl) GetError() error {
	return r.err
}

func (r *activeRecordImpl) ClearError() {
	if errors.Is(r.err, ErrRecordDeleted) {
		return
	}
	r.err = nil
}

type recordTransactionImpl struct {
	record           *activeRecordImpl
	snapshotData     []byte
	snapshotLocators []string
	newData          *[]byte
	locatorsToAdd    []string
	locatorsToRemove []string
	committed        bool
}

func (t *recordTransactionImpl) GetSnapshotData() []byte {
	return t.snapshotData
}

func (t *recordTransactionImpl) GetSnapshotLocators() []string {
	return t.snapshotLocators
}

func (t *recordTransactionImpl) SetData(data []byte) {
	t.newData = &data
}

func (t *recordTransactionImpl) AddLocator(locator string) error {
	if err := t.record.controller.validateLocator(locator); err != nil {
		return err
	}
	t.locatorsToAdd = append(t.locatorsToAdd, locator)
	return nil
}

func (t *recordTransactionImpl) RemoveLocator(locator string) error {
	if err := t.record.controller.validateLocator(locator); err != nil {
		return err
	}
	t.locatorsToRemove = append(t.locatorsToRemove, locator)
	return nil
}

func (t *recordTransactionImpl) Commit() error {
	if t.committed {
		return ErrTransactionAlreadyCommitted
	}

	if err := t.record.controller.checkTombstone(t.record.uniqueID); err != nil {
		return err
	}

	if t.newData == nil && len(t.locatorsToAdd) == 0 && len(t.locatorsToRemove) == 0 {
		return ErrTransactionNoChanges
	}

	dataKey := t.record.controller.buildRecordDataKey(t.record.uniqueID)

	if t.newData != nil {
		if t.snapshotData == nil {
			err := client.WithRetriesVoid(t.record.controller.ctx, t.record.controller.logger, func() error {
				return t.record.controller.insiClient.SetNX(dataKey, string(*t.newData))
			})
			if err != nil {
				if errors.Is(err, client.ErrConflict) {
					return ErrTransactionConflict
				}
				return fmt.Errorf("failed to set initial data for record %s: %w", t.record.uniqueID, err)
			}
		} else {
			err := client.WithRetriesVoid(t.record.controller.ctx, t.record.controller.logger, func() error {
				return t.record.controller.insiClient.CompareAndSwap(dataKey, string(t.snapshotData), string(*t.newData))
			})
			if err != nil {
				if errors.Is(err, client.ErrConflict) {
					return ErrTransactionConflict
				}
				return fmt.Errorf("failed to update data for record %s: %w", t.record.uniqueID, err)
			}
		}

		t.record.data = *t.newData
		t.record.lastDataUpdate = time.Now()
	}

	for _, locator := range t.locatorsToAdd {
		locatorKey := t.record.controller.buildLocatorKey(locator)

		err := client.WithRetriesVoid(t.record.controller.ctx, t.record.controller.logger, func() error {
			return t.record.controller.insiClient.SetNX(locatorKey, t.record.uniqueID)
		})
		if err != nil {
			if errors.Is(err, client.ErrConflict) {
				t.record.controller.logger.Warn("locator already exists during transaction commit", "locator", locator, "record", t.record.uniqueID)
				continue
			}
			t.record.controller.logger.Warn("failed to add locator during transaction commit", "locator", locator, "record", t.record.uniqueID, "error", err)
			continue
		}

		recordLocatorKey := t.record.controller.buildRecordLocatorKey(t.record.uniqueID, locator)
		err = client.WithRetriesVoid(t.record.controller.ctx, t.record.controller.logger, func() error {
			return t.record.controller.insiClient.Set(recordLocatorKey, "")
		})
		if err != nil {
			t.record.controller.logger.Warn("failed to create reverse locator mapping during transaction commit", "locator", locator, "record", t.record.uniqueID, "error", err)
		}
	}

	for _, locator := range t.locatorsToRemove {
		locatorKey := t.record.controller.buildLocatorKey(locator)
		recordLocatorKey := t.record.controller.buildRecordLocatorKey(t.record.uniqueID, locator)

		err := client.WithRetriesVoid(t.record.controller.ctx, t.record.controller.logger, func() error {
			return t.record.controller.insiClient.Delete(locatorKey)
		})
		if err != nil {
			t.record.controller.logger.Warn("failed to delete locator during transaction commit", "locator", locator, "record", t.record.uniqueID, "error", err)
		}

		err = client.WithRetriesVoid(t.record.controller.ctx, t.record.controller.logger, func() error {
			return t.record.controller.insiClient.Delete(recordLocatorKey)
		})
		if err != nil {
			t.record.controller.logger.Warn("failed to delete reverse locator mapping during transaction commit", "locator", locator, "record", t.record.uniqueID, "error", err)
		}
	}

	if len(t.locatorsToAdd) > 0 || len(t.locatorsToRemove) > 0 {
		t.record.lastLocatorUpdate = time.Time{}
	}

	t.committed = true
	return nil
}

func (t *recordTransactionImpl) Rollback() {
	t.newData = nil
	t.locatorsToAdd = make([]string, 0)
	t.locatorsToRemove = make([]string, 0)
}

func (r *recordControllerImpl) DeleteRecord(recordUUID string) error {
	if recordUUID == "" {
		return fmt.Errorf("recordUUID cannot be empty")
	}

	tombstoneKey := r.buildTombstoneKey(recordUUID)
	timestamp := fmt.Sprintf("%d", time.Now().Unix())

	err := client.WithRetriesVoid(r.ctx, r.logger, func() error {
		return r.insiClient.Set(tombstoneKey, timestamp)
	})
	if err != nil {
		return fmt.Errorf("failed to create tombstone for record %s: %w", recordUUID, err)
	}

	return nil
}

func (r *recordControllerImpl) IterateRecords(offset, limit int) ([]ActiveRecord, error) {
	locatorPrefix := fmt.Sprintf("%s:records:", r.prefix)

	keys, err := client.WithRetries(r.ctx, client.CONFIG_MAX_VOID_RETRIES, r.logger, func() ([]string, error) {
		return r.insiClient.IterateByPrefix(locatorPrefix, 0, 10000)
	})
	if err != nil {
		if errors.Is(err, client.ErrKeyNotFound) {
			return []ActiveRecord{}, nil
		}
		return nil, fmt.Errorf("failed to iterate records: %w", err)
	}

	recordUUIDs := make(map[string]bool)
	for _, key := range keys {
		if !strings.Contains(key, ":locator:") {
			continue
		}

		trimmed := strings.TrimPrefix(key, locatorPrefix)
		parts := strings.Split(trimmed, ":")
		if len(parts) >= 1 {
			recordUUID := parts[0]
			recordUUIDs[recordUUID] = true
		}
	}

	var validRecords []ActiveRecord
	for recordUUID := range recordUUIDs {
		if err := r.checkTombstone(recordUUID); err != nil {
			if errors.Is(err, ErrRecordDeleted) {
				continue
			}
			r.logger.Warn("error checking tombstone during iteration", "record", recordUUID, "error", err)
			continue
		}

		record := &activeRecordImpl{
			uniqueID:          recordUUID,
			data:              nil,
			locators:          nil,
			lastDataUpdate:    time.Time{},
			lastLocatorUpdate: time.Time{},
			controller:        r,
			err:               nil,
		}
		validRecords = append(validRecords, record)
	}

	if offset >= len(validRecords) {
		return []ActiveRecord{}, nil
	}

	end := offset + limit
	if end > len(validRecords) {
		end = len(validRecords)
	}

	return validRecords[offset:end], nil
}

func (r *recordControllerImpl) CleanupDeletedRecord(recordUUID string) error {
	if recordUUID == "" {
		return fmt.Errorf("recordUUID cannot be empty")
	}

	if err := r.checkTombstone(recordUUID); err != nil {
		if !errors.Is(err, ErrRecordDeleted) {
			return fmt.Errorf("record %s is not tombstoned: %w", recordUUID, err)
		}
	} else {
		return fmt.Errorf("record %s has no tombstone", recordUUID)
	}

	locatorPrefix := fmt.Sprintf("%s:records:%s:locator:", r.prefix, recordUUID)
	locatorKeys, err := client.WithRetries(r.ctx, client.CONFIG_MAX_VOID_RETRIES, r.logger, func() ([]string, error) {
		return r.insiClient.IterateByPrefix(locatorPrefix, 0, 1000)
	})
	if err != nil && !errors.Is(err, client.ErrKeyNotFound) {
		return fmt.Errorf("failed to iterate locators for record %s: %w", recordUUID, err)
	}

	for _, locatorKey := range locatorKeys {
		locator := strings.TrimPrefix(locatorKey, locatorPrefix)

		forwardKey := r.buildLocatorKey(locator)
		err := client.WithRetriesVoid(r.ctx, r.logger, func() error {
			return r.insiClient.Delete(forwardKey)
		})
		if err != nil {
			r.logger.Warn("failed to delete forward locator mapping during cleanup", "locator", locator, "record", recordUUID, "error", err)
		}

		err = client.WithRetriesVoid(r.ctx, r.logger, func() error {
			return r.insiClient.Delete(locatorKey)
		})
		if err != nil {
			r.logger.Warn("failed to delete reverse locator mapping during cleanup", "locator", locator, "record", recordUUID, "error", err)
		}
	}

	dataKey := r.buildRecordDataKey(recordUUID)
	err = client.WithRetriesVoid(r.ctx, r.logger, func() error {
		return r.insiClient.Delete(dataKey)
	})
	if err != nil {
		r.logger.Warn("failed to delete record data during cleanup", "record", recordUUID, "error", err)
	}

	tombstoneKey := r.buildTombstoneKey(recordUUID)
	err = client.WithRetriesVoid(r.ctx, r.logger, func() error {
		return r.insiClient.Delete(tombstoneKey)
	})
	if err != nil {
		return fmt.Errorf("failed to delete tombstone for record %s: %w", recordUUID, err)
	}

	return nil
}

func (r *recordControllerImpl) iterateTombstones(offset, limit int) ([]string, error) {
	tombstonePrefix := fmt.Sprintf("%s:tombstones:", r.prefix)

	keys, err := client.WithRetries(r.ctx, client.CONFIG_MAX_VOID_RETRIES, r.logger, func() ([]string, error) {
		return r.insiClient.IterateByPrefix(tombstonePrefix, offset, limit)
	})
	if err != nil {
		if errors.Is(err, client.ErrKeyNotFound) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to iterate tombstones: %w", err)
	}

	recordUUIDs := make([]string, 0, len(keys))
	for _, key := range keys {
		recordUUID := strings.TrimPrefix(key, tombstonePrefix)
		recordUUIDs = append(recordUUIDs, recordUUID)
	}

	return recordUUIDs, nil
}

func (r *recordControllerImpl) tombstoneCleanupWorker() {
	defer close(r.stoppedChan)

	r.logger.Info("tombstone cleanup worker started", "prefix", r.prefix)

	r.processTombstones()

	for {
		jitter := time.Duration(0)
		if r.cleanupJitter > 0 {
			jitter = time.Duration(rand.Int63n(int64(r.cleanupJitter)))
		}
		interval := r.cleanupInterval + jitter

		timer := time.NewTimer(interval)

		select {
		case <-r.stopChan:
			timer.Stop()
			r.logger.Info("tombstone cleanup worker stopping", "prefix", r.prefix)
			return
		case <-timer.C:
			r.processTombstones()
		}
	}
}

func (r *recordControllerImpl) processTombstones() {
	tombstones, err := r.iterateTombstones(0, 100)
	if err != nil {
		r.logger.Error("failed to iterate tombstones", "error", err)
		return
	}

	if len(tombstones) == 0 {
		return
	}

	r.logger.Debug("processing tombstones", "count", len(tombstones))

	for _, recordUUID := range tombstones {
		err := r.CleanupDeletedRecord(recordUUID)
		if err != nil {
			r.logger.Warn("failed to cleanup tombstoned record", "record", recordUUID, "error", err)
		} else {
			r.logger.Debug("cleaned up tombstoned record", "record", recordUUID)
		}
	}
}

func (r *recordControllerImpl) Start() {
	go r.tombstoneCleanupWorker()
}

func (r *recordControllerImpl) Stop() {
	close(r.stopChan)
	<-r.stoppedChan

	r.logger.Info("running final tombstone cleanup", "prefix", r.prefix)
	r.processTombstones()

	r.logger.Info("tombstone cleanup worker stopped", "prefix", r.prefix)
}
