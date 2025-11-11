package objects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/InsulaLabs/ferry/pkg/datascape/records"

	"github.com/InsulaLabs/insi/client"
)

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

type ObjectSchema struct {
	UniqueLocatorCategories         []string
	BundledLocatorCategories        []string
	ExternalRecordLocatorCategories []string
}

type Object struct {
	Data             []byte            `json:"data"`
	Version          int               `json:"version"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	ExternalLocators map[string]string `json:"external_locators,omitempty"`
}

type ObjectController struct {
	recordGroup records.RecordController
	schema      ObjectSchema
	logger      *slog.Logger
	typename    string
}

type ObjectTransaction interface {
	GetSnapshot() *Object

	UpdateData(data []byte)
	AddUniqueLocator(category, value string) error
	RemoveUniqueLocator(category, value string) error
	UpdateUniqueLocator(category, oldValue, newValue string) error
	AddBundledLocator(category, value string) error
	RemoveBundledLocator(category, value string) error
	SetExternalRecordLocator(category, value string) error
	RemoveExternalRecordLocator(category string) error

	Commit() error
	Rollback()
}

func NewObjectController(
	ctx context.Context,
	typename string,
	schema ObjectSchema,
	cacheDuration time.Duration,
	cleanupInterval time.Duration,
	cleanupJitter time.Duration,
	parentLogger *slog.Logger,
	insiClient *client.Client,
) *ObjectController {
	logger := parentLogger.WithGroup(typename)

	recordPrefix := fmt.Sprintf("objects:%s", typename)

	recordGroup := records.NewRecordController(
		ctx,
		recordPrefix,
		cacheDuration,
		cleanupInterval,
		cleanupJitter,
		logger.WithGroup("records"),
		insiClient,
	)

	return &ObjectController{
		recordGroup: recordGroup,
		schema:      schema,
		logger:      logger,
		typename:    typename,
	}
}

func (oc *ObjectController) isCategoryInSchema(category string, categoryType string) bool {
	var categories []string
	switch categoryType {
	case "unique":
		categories = oc.schema.UniqueLocatorCategories
	case "bundled":
		categories = oc.schema.BundledLocatorCategories
	case "external":
		categories = oc.schema.ExternalRecordLocatorCategories
	default:
		return false
	}

	for _, c := range categories {
		if c == category {
			return true
		}
	}
	return false
}

type externalLocatorOperation struct {
	category string
	value    string
	isRemove bool
}

type objectTransactionImpl struct {
	controller              *ObjectController
	recordTxn               records.RecordTransaction
	snapshotObject          Object
	pendingObjectData       *[]byte
	externalLocatorOps      []externalLocatorOperation
	uniqueLocatorsToAdd     map[string]string
	uniqueLocatorsToRemove  map[string]string
	bundledLocatorsToAdd    map[string]string
	bundledLocatorsToRemove map[string]string
}

func (t *objectTransactionImpl) GetSnapshot() *Object {
	snapshot := t.snapshotObject
	return &snapshot
}

func (t *objectTransactionImpl) UpdateData(data []byte) {
	t.pendingObjectData = &data
}

func (t *objectTransactionImpl) AddUniqueLocator(category, value string) error {
	if !t.controller.isCategoryInSchema(category, "unique") {
		return ErrCategoryNotInSchema
	}
	key := fmt.Sprintf("%s:%s", category, value)
	t.uniqueLocatorsToAdd[key] = value
	delete(t.uniqueLocatorsToRemove, key)
	return nil
}

func (t *objectTransactionImpl) RemoveUniqueLocator(category, value string) error {
	if !t.controller.isCategoryInSchema(category, "unique") {
		return ErrCategoryNotInSchema
	}
	key := fmt.Sprintf("%s:%s", category, value)
	t.uniqueLocatorsToRemove[key] = value
	delete(t.uniqueLocatorsToAdd, key)
	return nil
}

func (t *objectTransactionImpl) UpdateUniqueLocator(category, oldValue, newValue string) error {
	if !t.controller.isCategoryInSchema(category, "unique") {
		return ErrCategoryNotInSchema
	}
	oldKey := fmt.Sprintf("%s:%s", category, oldValue)
	newKey := fmt.Sprintf("%s:%s", category, newValue)
	t.uniqueLocatorsToRemove[oldKey] = oldValue
	t.uniqueLocatorsToAdd[newKey] = newValue
	return nil
}

func (t *objectTransactionImpl) AddBundledLocator(category, value string) error {
	if !t.controller.isCategoryInSchema(category, "bundled") {
		return ErrCategoryNotInSchema
	}
	key := fmt.Sprintf("%s:%s", category, value)
	t.bundledLocatorsToAdd[key] = value
	delete(t.bundledLocatorsToRemove, key)
	return nil
}

func (t *objectTransactionImpl) RemoveBundledLocator(category, value string) error {
	if !t.controller.isCategoryInSchema(category, "bundled") {
		return ErrCategoryNotInSchema
	}
	key := fmt.Sprintf("%s:%s", category, value)
	t.bundledLocatorsToRemove[key] = value
	delete(t.bundledLocatorsToAdd, key)
	return nil
}

func (t *objectTransactionImpl) SetExternalRecordLocator(category, value string) error {
	if !t.controller.isCategoryInSchema(category, "external") {
		return ErrCategoryNotInSchema
	}
	t.externalLocatorOps = append(t.externalLocatorOps, externalLocatorOperation{
		category: category,
		value:    value,
		isRemove: false,
	})
	return nil
}

func (t *objectTransactionImpl) RemoveExternalRecordLocator(category string) error {
	if !t.controller.isCategoryInSchema(category, "external") {
		return ErrCategoryNotInSchema
	}
	t.externalLocatorOps = append(t.externalLocatorOps, externalLocatorOperation{
		category: category,
		value:    "",
		isRemove: true,
	})
	return nil
}

func (t *objectTransactionImpl) Commit() error {
	obj := t.snapshotObject

	if t.pendingObjectData != nil {
		obj.Data = *t.pendingObjectData
		obj.UpdatedAt = time.Now()
	}

	if len(t.externalLocatorOps) > 0 {
		if obj.ExternalLocators == nil {
			obj.ExternalLocators = make(map[string]string)
		}

		for _, op := range t.externalLocatorOps {
			if op.isRemove {
				delete(obj.ExternalLocators, op.category)
			} else {
				obj.ExternalLocators[op.category] = op.value
			}
		}
		obj.UpdatedAt = time.Now()
	}

	jsonData, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %w", err)
	}

	t.recordTxn.SetData(jsonData)

	for key := range t.uniqueLocatorsToAdd {
		if err := t.recordTxn.AddLocator(key); err != nil {
			return fmt.Errorf("failed to stage unique locator add: %w", err)
		}
	}

	for key := range t.uniqueLocatorsToRemove {
		if err := t.recordTxn.RemoveLocator(key); err != nil {
			return fmt.Errorf("failed to stage unique locator remove: %w", err)
		}
	}

	for key := range t.bundledLocatorsToAdd {
		if err := t.recordTxn.AddLocator(key); err != nil {
			return fmt.Errorf("failed to stage bundled locator add: %w", err)
		}
	}

	for key := range t.bundledLocatorsToRemove {
		if err := t.recordTxn.RemoveLocator(key); err != nil {
			return fmt.Errorf("failed to stage bundled locator remove: %w", err)
		}
	}

	return t.recordTxn.Commit()
}

func (t *objectTransactionImpl) Rollback() {
	t.recordTxn.Rollback()
	t.pendingObjectData = nil
	t.externalLocatorOps = make([]externalLocatorOperation, 0)
	t.uniqueLocatorsToAdd = make(map[string]string)
	t.uniqueLocatorsToRemove = make(map[string]string)
	t.bundledLocatorsToAdd = make(map[string]string)
	t.bundledLocatorsToRemove = make(map[string]string)
}

func (oc *ObjectController) Start() {
	oc.recordGroup.Start()
}

func (oc *ObjectController) Stop() {
	if oc.recordGroup != nil {
		oc.recordGroup.Stop()
	}
}

func (oc *ObjectController) BeginTransaction(locator string) (ObjectTransaction, error) {
	objectRecord, err := oc.recordGroup.GetRecordByLocator(locator)
	if err != nil {
		if errors.Is(err, records.ErrRecordNotFound) || errors.Is(err, records.ErrRecordDeleted) {
			return nil, ErrObjectNotFound
		}
		oc.logger.Error("failed to get object record for transaction", "locator", locator, "error", err)
		return nil, ErrFailedToLoadObject
	}

	recordTxn, err := objectRecord.BeginTransaction()
	if err != nil {
		oc.logger.Error("failed to begin record transaction", "locator", locator, "error", err)
		return nil, ErrFailedToLoadObject
	}

	snapshotData := recordTxn.GetSnapshotData()

	var obj Object
	if len(snapshotData) > 0 {
		if err := json.Unmarshal(snapshotData, &obj); err != nil {
			oc.logger.Error("failed to unmarshal object snapshot", "locator", locator, "error", err)
			return nil, ErrFailedToLoadObject
		}
	}

	oc.logger.Debug("transaction started", "locator", locator)

	return &objectTransactionImpl{
		controller:              oc,
		recordTxn:               recordTxn,
		snapshotObject:          obj,
		pendingObjectData:       nil,
		externalLocatorOps:      make([]externalLocatorOperation, 0),
		uniqueLocatorsToAdd:     make(map[string]string),
		uniqueLocatorsToRemove:  make(map[string]string),
		bundledLocatorsToAdd:    make(map[string]string),
		bundledLocatorsToRemove: make(map[string]string),
	}, nil
}

func (oc *ObjectController) CreateObject(primaryLocator string, initialData []byte) (*Object, error) {
	objectRecordHandle, err := oc.recordGroup.CreateNewRecordWithLocator(primaryLocator)
	if err != nil {
		if errors.Is(err, records.ErrNewRecordLocatorNotUnique) {
			return nil, ErrLocatorAlreadyExists
		}
		return nil, fmt.Errorf("failed to create new object record: %w", err)
	}

	obj := Object{
		Data:      initialData,
		Version:   1,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	jsonData, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal object data: %w", err)
	}

	txn, err := objectRecordHandle.BeginTransaction()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction for new object: %w", err)
	}

	txn.SetData(jsonData)

	err = txn.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to commit object data: %w", err)
	}

	oc.logger.Info("object created", "primaryLocator", primaryLocator)

	return &obj, nil
}

func (oc *ObjectController) GetObjectByLocator(locator string) (*Object, error) {
	objectRecord, err := oc.recordGroup.GetRecordByLocator(locator)
	if err != nil {
		if errors.Is(err, records.ErrRecordNotFound) || errors.Is(err, records.ErrRecordDeleted) {
			return nil, ErrObjectNotFound
		}
		oc.logger.Error("failed to get object record", "error", err)
		return nil, ErrFailedToLoadObject
	}

	objectDataRaw := objectRecord.GetData(false)
	if objectRecord.HasError() {
		oc.logger.Error("failed to load object data", "error", objectRecord.GetError())
		return nil, ErrFailedToLoadObject
	}

	var obj Object
	err = json.Unmarshal(objectDataRaw, &obj)
	if err != nil {
		oc.logger.Error("failed to unmarshal object data", "error", err)
		return nil, ErrFailedToLoadObject
	}

	return &obj, nil
}

func (oc *ObjectController) UpdateObjectData(locator string, data []byte) error {
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(locator)
		if err != nil {
			return err
		}

		txn.UpdateData(data)

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("object data updated", "locator", locator)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "locator", locator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to update object data", "locator", locator, "error", err)
		return ErrFailedToUpdateObject
	}

	oc.logger.Error("failed to update object data after max retries", "locator", locator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to update after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) DeleteObject(locator string) error {
	objectRecord, err := oc.recordGroup.GetRecordByLocator(locator)
	if err != nil {
		if errors.Is(err, records.ErrRecordNotFound) || errors.Is(err, records.ErrRecordDeleted) {
			return nil
		}
		oc.logger.Error("failed to get object record", "error", err)
		return ErrFailedToLoadObject
	}

	err = oc.recordGroup.DeleteRecord(objectRecord.GetUniqueID())
	if err != nil {
		oc.logger.Error("failed to delete object record", "error", err)
		return ErrFailedToDeleteObject
	}

	oc.logger.Info("object deleted", "locator", locator)

	return nil
}

func (oc *ObjectController) AddUniqueLocator(existingLocator string, category string, newLocator string) error {
	if !oc.isCategoryInSchema(category, "unique") {
		return ErrCategoryNotInSchema
	}

	newLocatorFormatted := fmt.Sprintf("%s:%s", category, newLocator)

	_, err := oc.recordGroup.GetRecordByLocator(newLocatorFormatted)
	if err == nil {
		return ErrLocatorAlreadyExists
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.AddUniqueLocator(category, newLocator); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("unique locator added", "existingLocator", existingLocator, "category", category, "newLocator", newLocator)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to add unique locator", "existingLocator", existingLocator, "error", err)
		return fmt.Errorf("failed to add unique locator: %w", err)
	}

	oc.logger.Error("failed to add unique locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to add unique locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) RemoveUniqueLocator(existingLocator string, category string, locatorToRemove string) error {
	if !oc.isCategoryInSchema(category, "unique") {
		return ErrCategoryNotInSchema
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.RemoveUniqueLocator(category, locatorToRemove); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("unique locator removed", "existingLocator", existingLocator, "category", category, "locatorToRemove", locatorToRemove)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to remove unique locator", "existingLocator", existingLocator, "error", err)
		return fmt.Errorf("failed to remove unique locator: %w", err)
	}

	oc.logger.Error("failed to remove unique locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to remove unique locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) UpdateUniqueLocator(existingLocator string, category string, oldValue string, newValue string) error {
	if !oc.isCategoryInSchema(category, "unique") {
		return ErrCategoryNotInSchema
	}

	newLocatorFormatted := fmt.Sprintf("%s:%s", category, newValue)

	_, err := oc.recordGroup.GetRecordByLocator(newLocatorFormatted)
	if err == nil {
		return ErrLocatorAlreadyExists
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.UpdateUniqueLocator(category, oldValue, newValue); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("unique locator updated", "existingLocator", existingLocator, "category", category, "oldValue", oldValue, "newValue", newValue)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to update unique locator", "existingLocator", existingLocator, "error", err)
		return fmt.Errorf("failed to update unique locator: %w", err)
	}

	oc.logger.Error("failed to update unique locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to update unique locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) AddBundledLocator(existingLocator string, category string, value string) error {
	if !oc.isCategoryInSchema(category, "bundled") {
		return ErrCategoryNotInSchema
	}

	valueLocatorFormatted := fmt.Sprintf("%s:%s", category, value)

	_, err := oc.recordGroup.GetRecordByLocator(valueLocatorFormatted)
	if err == nil {
		return ErrLocatorAlreadyExists
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.AddBundledLocator(category, value); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("bundled locator added", "existingLocator", existingLocator, "category", category, "value", value)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to add bundled locator", "existingLocator", existingLocator, "error", err)
		return fmt.Errorf("failed to add bundled locator: %w", err)
	}

	oc.logger.Error("failed to add bundled locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to add bundled locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) RemoveBundledLocator(existingLocator string, category string, value string) error {
	if !oc.isCategoryInSchema(category, "bundled") {
		return ErrCategoryNotInSchema
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.RemoveBundledLocator(category, value); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("bundled locator removed", "existingLocator", existingLocator, "category", category, "value", value)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to remove bundled locator", "existingLocator", existingLocator, "error", err)
		return fmt.Errorf("failed to remove bundled locator: %w", err)
	}

	oc.logger.Error("failed to remove bundled locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to remove bundled locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) SetExternalRecordLocator(existingLocator string, category string, value string) error {
	if !oc.isCategoryInSchema(category, "external") {
		return ErrCategoryNotInSchema
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.SetExternalRecordLocator(category, value); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("external record locator set", "existingLocator", existingLocator, "category", category, "value", value)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to set external record locator", "existingLocator", existingLocator, "error", err)
		return ErrFailedToUpdateObject
	}

	oc.logger.Error("failed to set external record locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to set external record locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) GetExternalRecordLocator(existingLocator string, category string) (string, error) {
	if !oc.isCategoryInSchema(category, "external") {
		return "", ErrCategoryNotInSchema
	}

	objectRecord, err := oc.recordGroup.GetRecordByLocator(existingLocator)
	if err != nil {
		if errors.Is(err, records.ErrRecordNotFound) || errors.Is(err, records.ErrRecordDeleted) {
			return "", ErrObjectNotFound
		}
		oc.logger.Error("failed to get object record", "error", err)
		return "", ErrFailedToLoadObject
	}

	objectDataRaw := objectRecord.GetData(false)
	if objectRecord.HasError() {
		oc.logger.Error("failed to load object data", "error", objectRecord.GetError())
		return "", ErrFailedToLoadObject
	}

	var obj Object
	err = json.Unmarshal(objectDataRaw, &obj)
	if err != nil {
		oc.logger.Error("failed to unmarshal object data", "error", err)
		return "", ErrFailedToLoadObject
	}

	if obj.ExternalLocators == nil {
		return "", ErrLocatorNotFound
	}

	value, exists := obj.ExternalLocators[category]
	if !exists || value == "" {
		return "", ErrLocatorNotFound
	}

	return value, nil
}

func (oc *ObjectController) RemoveExternalRecordLocator(existingLocator string, category string) error {
	if !oc.isCategoryInSchema(category, "external") {
		return ErrCategoryNotInSchema
	}

	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		txn, err := oc.BeginTransaction(existingLocator)
		if err != nil {
			return err
		}

		if err := txn.RemoveExternalRecordLocator(category); err != nil {
			return err
		}

		err = txn.Commit()
		if err == nil {
			oc.logger.Info("external record locator removed", "existingLocator", existingLocator, "category", category)
			return nil
		}

		if errors.Is(err, records.ErrTransactionConflict) {
			oc.logger.Warn("transaction conflict, retrying", "existingLocator", existingLocator, "attempt", attempt+1)
			continue
		}

		oc.logger.Error("failed to remove external record locator", "existingLocator", existingLocator, "error", err)
		return ErrFailedToUpdateObject
	}

	oc.logger.Error("failed to remove external record locator after max retries", "existingLocator", existingLocator, "maxRetries", maxRetries)
	return fmt.Errorf("failed to remove external record locator after %d retries due to conflicts", maxRetries)
}

func (oc *ObjectController) IterateObjects(offset, limit int) ([]*Object, error) {
	activeRecords, err := oc.recordGroup.IterateRecords(offset, limit)
	if err != nil {
		oc.logger.Error("failed to iterate records", "error", err)
		return nil, fmt.Errorf("failed to iterate object records: %w", err)
	}

	objects := make([]*Object, 0, len(activeRecords))
	for _, record := range activeRecords {
		objectDataRaw := record.GetData(false)
		if record.HasError() {
			oc.logger.Warn("failed to get object data", "error", record.GetError())
			continue
		}

		var obj Object
		err = json.Unmarshal(objectDataRaw, &obj)
		if err != nil {
			oc.logger.Warn("failed to unmarshal object data", "error", err)
			continue
		}

		objects = append(objects, &obj)
	}

	return objects, nil
}
