package records

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/InsulaLabs/insi/client"
)

func getTestClient(t *testing.T) *client.Client {
	apiKey := os.Getenv("INSI_API_KEY")
	if apiKey == "" {
		t.Skip("INSI_API_KEY not set, skipping test")
	}

	testLogger := getTestLogger()
	insiClient, err := client.NewClient(&client.Config{
		ConnectionType: client.ConnectionTypeDirect,
		Logger:         testLogger.WithGroup("insi"),
		ApiKey:         apiKey,
		Endpoints: []client.Endpoint{
			{
				PublicBinding:  "red.insulalabs.io:443",
				PrivateBinding: "red.insulalabs.io:443",
				ClientDomain:   "red.insulalabs.io",
				Logger:         testLogger.WithGroup("insi-endpoint-red"),
			},
			{
				PublicBinding:  "blue.insulalabs.io:443",
				PrivateBinding: "blue.insulalabs.io:443",
				ClientDomain:   "blue.insulalabs.io",
				Logger:         testLogger.WithGroup("insi-endpoint-blue"),
			},
			{
				PublicBinding:  "green.insulalabs.io:443",
				PrivateBinding: "green.insulalabs.io:443",
				ClientDomain:   "green.insulalabs.io",
				Logger:         testLogger.WithGroup("insi-endpoint-green"),
			},
		},
		SkipVerify:             false,
		Timeout:                10 * time.Second,
		EnableLeaderStickiness: false,
		DisableRedirects:       false,
	})
	if err != nil {
		t.Fatalf("failed to create INSI client: %v", err)
	}

	return insiClient
}

func getTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
}

var idCounter int64

func generateUniqueID() string {
	idCounter++
	return fmt.Sprintf("test-%d-%d", time.Now().UnixNano(), idCounter)
}

func testSleep() {
	time.Sleep(100 * time.Millisecond)
}

func getTestRecordController(t *testing.T) RecordController {
	ctx := context.Background()
	client := getTestClient(t)
	logger := getTestLogger()
	prefix := generateUniqueID()

	controller := NewRecordController(
		ctx,
		prefix,
		5*time.Second,
		60*time.Second,
		30*time.Second,
		logger,
		client,
	)

	controller.Start()
	t.Cleanup(func() {
		controller.Stop()
	})

	return controller
}

func TestRecordController_CreateAndGet(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	if record.GetUniqueID() == "" {
		t.Fatal("record UUID should not be empty")
	}

	retrieved, err := controller.GetRecordByLocator(locator)
	if err != nil {
		t.Fatalf("failed to get record by locator: %v", err)
	}

	if retrieved.GetUniqueID() != record.GetUniqueID() {
		t.Errorf("UUID mismatch: got %s, want %s", retrieved.GetUniqueID(), record.GetUniqueID())
	}
}

func TestRecordController_CreateDuplicate(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	_, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create first record: %v", err)
	}
	testSleep()

	_, err = controller.CreateNewRecordWithLocator(locator)
	if err != ErrNewRecordLocatorNotUnique {
		t.Errorf("expected ErrNewRecordLocatorNotUnique, got: %v", err)
	}
}

func TestRecordController_GetNonExistent(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	_, err := controller.GetRecordByLocator(locator)
	if err != ErrRecordNotFound {
		t.Errorf("expected ErrRecordNotFound, got: %v", err)
	}
}

func TestRecordTransaction_SetData(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	testData := []byte("test-data-payload")
	txn.SetData(testData)

	err = txn.Commit()
	if err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	txn2, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin second transaction: %v", err)
	}

	snapshot := txn2.GetSnapshotData()
	if string(snapshot) != string(testData) {
		t.Errorf("data mismatch: got %s, want %s", string(snapshot), string(testData))
	}
}

func TestRecordTransaction_SetDataEmpty(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if len(txn.GetSnapshotData()) != 0 {
		t.Error("snapshot should be empty for new record")
	}

	testData := []byte("initial-data")
	txn.SetData(testData)

	err = txn.Commit()
	if err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	data := record.GetData(true)
	if string(data) != string(testData) {
		t.Errorf("data mismatch: got %s, want %s", string(data), string(testData))
	}
}

func TestRecordTransaction_UpdateData(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.SetData([]byte("v1"))
	if err := txn1.Commit(); err != nil {
		t.Fatalf("failed to commit transaction 1: %v", err)
	}
	testSleep()

	txn2, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 2: %v", err)
	}

	if string(txn2.GetSnapshotData()) != "v1" {
		t.Errorf("snapshot mismatch: got %s, want v1", string(txn2.GetSnapshotData()))
	}

	txn2.SetData([]byte("v2"))
	if err := txn2.Commit(); err != nil {
		t.Fatalf("failed to commit transaction 2: %v", err)
	}
	testSleep()

	txn3, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 3: %v", err)
	}

	if string(txn3.GetSnapshotData()) != "v2" {
		t.Errorf("snapshot mismatch: got %s, want v2", string(txn3.GetSnapshotData()))
	}
}

func TestRecordTransaction_NoChanges(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	err = txn.Commit()
	if err != ErrTransactionNoChanges {
		t.Errorf("expected ErrTransactionNoChanges, got: %v", err)
	}
}

func TestRecordTransaction_AlreadyCommitted(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	txn.SetData([]byte("test"))

	err = txn.Commit()
	if err != nil {
		t.Fatalf("failed to commit first time: %v", err)
	}

	err = txn.Commit()
	if err != ErrTransactionAlreadyCommitted {
		t.Errorf("expected ErrTransactionAlreadyCommitted, got: %v", err)
	}
}

func TestRecordTransaction_AddLocator(t *testing.T) {
	controller := getTestRecordController(t)
	primaryLocator := generateUniqueID()
	secondaryLocator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(primaryLocator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	originalUUID := record.GetUniqueID()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if err := txn.AddLocator(secondaryLocator); err != nil {
		t.Fatalf("failed to add locator: %v", err)
	}

	if err := txn.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	retrievedBySecondary, err := controller.GetRecordByLocator(secondaryLocator)
	if err != nil {
		t.Fatalf("failed to get record by secondary locator: %v", err)
	}

	if retrievedBySecondary.GetUniqueID() != originalUUID {
		t.Errorf("UUID mismatch: got %s, want %s", retrievedBySecondary.GetUniqueID(), originalUUID)
	}
}

func TestRecordTransaction_AddMultipleLocators(t *testing.T) {
	controller := getTestRecordController(t)
	primaryLocator := generateUniqueID()
	locator1 := generateUniqueID()
	locator2 := generateUniqueID()
	locator3 := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(primaryLocator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	originalUUID := record.GetUniqueID()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	if err := txn.AddLocator(locator1); err != nil {
		t.Fatalf("failed to add locator1: %v", err)
	}
	if err := txn.AddLocator(locator2); err != nil {
		t.Fatalf("failed to add locator2: %v", err)
	}
	if err := txn.AddLocator(locator3); err != nil {
		t.Fatalf("failed to add locator3: %v", err)
	}

	if err := txn.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	for i, loc := range []string{locator1, locator2, locator3} {
		retrieved, err := controller.GetRecordByLocator(loc)
		if err != nil {
			t.Errorf("failed to get record by locator%d: %v", i+1, err)
			continue
		}
		if retrieved.GetUniqueID() != originalUUID {
			t.Errorf("UUID mismatch for locator%d: got %s, want %s", i+1, retrieved.GetUniqueID(), originalUUID)
		}
	}
}

func TestRecordTransaction_RemoveLocator(t *testing.T) {
	controller := getTestRecordController(t)
	primaryLocator := generateUniqueID()
	secondaryLocator := generateUniqueID()
	tertiaryLocator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(primaryLocator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.AddLocator(secondaryLocator)
	txn1.AddLocator(tertiaryLocator)
	if err := txn1.Commit(); err != nil {
		t.Fatalf("failed to commit transaction 1: %v", err)
	}
	testSleep()

	txn2, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 2: %v", err)
	}
	if err := txn2.RemoveLocator(secondaryLocator); err != nil {
		t.Fatalf("failed to remove locator: %v", err)
	}
	if err := txn2.Commit(); err != nil {
		t.Fatalf("failed to commit transaction 2: %v", err)
	}
	testSleep()

	_, err = controller.GetRecordByLocator(secondaryLocator)
	if err != ErrRecordNotFound {
		t.Errorf("expected ErrRecordNotFound for removed locator, got: %v", err)
	}

	_, err = controller.GetRecordByLocator(primaryLocator)
	if err != nil {
		t.Errorf("primary locator should still work: %v", err)
	}

	_, err = controller.GetRecordByLocator(tertiaryLocator)
	if err != nil {
		t.Errorf("tertiary locator should still work: %v", err)
	}
}

func TestRecordTransaction_AddAndRemoveLocators(t *testing.T) {
	controller := getTestRecordController(t)
	primaryLocator := generateUniqueID()
	locatorA := generateUniqueID()
	locatorB := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(primaryLocator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	originalUUID := record.GetUniqueID()

	txn, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	txn.AddLocator(locatorA)
	txn.AddLocator(locatorB)
	txn.RemoveLocator(primaryLocator)

	if err := txn.Commit(); err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	_, err = controller.GetRecordByLocator(primaryLocator)
	if err != ErrRecordNotFound {
		t.Errorf("primary locator should be removed, got: %v", err)
	}

	recA, err := controller.GetRecordByLocator(locatorA)
	if err != nil {
		t.Fatalf("locator A should work: %v", err)
	}
	if recA.GetUniqueID() != originalUUID {
		t.Errorf("UUID mismatch for locator A")
	}

	recB, err := controller.GetRecordByLocator(locatorB)
	if err != nil {
		t.Fatalf("locator B should work: %v", err)
	}
	if recB.GetUniqueID() != originalUUID {
		t.Errorf("UUID mismatch for locator B")
	}
}

func TestRecordTransaction_ConcurrentModification(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.SetData([]byte("initial"))
	if err := txn1.Commit(); err != nil {
		t.Fatalf("failed to commit initial data: %v", err)
	}
	testSleep()

	txnA, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction A: %v", err)
	}

	txnB, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction B: %v", err)
	}

	txnB.SetData([]byte("from-B"))
	if err := txnB.Commit(); err != nil {
		t.Fatalf("failed to commit transaction B: %v", err)
	}
	testSleep()

	txnA.SetData([]byte("from-A"))
	err = txnA.Commit()
	if err != ErrTransactionConflict {
		t.Errorf("expected ErrTransactionConflict, got: %v", err)
	}
}

func TestRecordTransaction_RetryAfterConflict(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.SetData([]byte("v1"))
	if err := txn1.Commit(); err != nil {
		t.Fatalf("failed to commit v1: %v", err)
	}
	testSleep()

	txnA, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction A: %v", err)
	}

	txnB, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction B: %v", err)
	}
	txnB.SetData([]byte("v2"))
	if err := txnB.Commit(); err != nil {
		t.Fatalf("failed to commit v2: %v", err)
	}
	testSleep()

	txnA.SetData([]byte("v3-attempt"))
	err = txnA.Commit()
	if err != ErrTransactionConflict {
		t.Errorf("expected conflict, got: %v", err)
	}

	txnRetry, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin retry transaction: %v", err)
	}

	if string(txnRetry.GetSnapshotData()) != "v2" {
		t.Errorf("snapshot should be v2, got: %s", string(txnRetry.GetSnapshotData()))
	}

	txnRetry.SetData([]byte("v3-retry"))
	if err := txnRetry.Commit(); err != nil {
		t.Fatalf("failed to commit retry: %v", err)
	}
	testSleep()

	finalData := record.GetData(true)
	if string(finalData) != "v3-retry" {
		t.Errorf("final data mismatch: got %s, want v3-retry", string(finalData))
	}
}

func TestRecordTransaction_LocatorConflict(t *testing.T) {
	controller := getTestRecordController(t)
	locatorA := generateUniqueID()
	locatorB := generateUniqueID()
	conflictLocator := generateUniqueID()

	recordA, err := controller.CreateNewRecordWithLocator(locatorA)
	if err != nil {
		t.Fatalf("failed to create record A: %v", err)
	}
	testSleep()

	txnA, err := recordA.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction A: %v", err)
	}
	txnA.AddLocator(conflictLocator)
	if err := txnA.Commit(); err != nil {
		t.Fatalf("failed to commit transaction A: %v", err)
	}
	testSleep()

	recordB, err := controller.CreateNewRecordWithLocator(locatorB)
	if err != nil {
		t.Fatalf("failed to create record B: %v", err)
	}
	testSleep()

	txnB, err := recordB.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction B: %v", err)
	}
	txnB.AddLocator(conflictLocator)
	err = txnB.Commit()
	if err != nil {
		t.Logf("locator conflict during commit resulted in error: %v", err)
	}
}

func TestRecordTransaction_Rollback(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()
	newLocator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.SetData([]byte("initial"))
	if err := txn1.Commit(); err != nil {
		t.Fatalf("failed to commit initial data: %v", err)
	}
	testSleep()

	txn2, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 2: %v", err)
	}

	txn2.SetData([]byte("should-be-rolled-back"))
	txn2.AddLocator(newLocator)

	txn2.Rollback()
	testSleep()

	data := record.GetData(true)
	if string(data) != "initial" {
		t.Errorf("data should not have changed, got: %s", string(data))
	}

	_, err = controller.GetRecordByLocator(newLocator)
	if err != ErrRecordNotFound {
		t.Errorf("new locator should not exist after rollback, got: %v", err)
	}
}

func TestRecordTransaction_RollbackThenNewTransaction(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.SetData([]byte("rollback-me"))
	txn1.Rollback()

	txn2, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 2: %v", err)
	}
	txn2.SetData([]byte("commit-me"))
	if err := txn2.Commit(); err != nil {
		t.Fatalf("failed to commit transaction 2: %v", err)
	}
	testSleep()

	data := record.GetData(true)
	if string(data) != "commit-me" {
		t.Errorf("data mismatch: got %s, want commit-me", string(data))
	}
}

func TestRecordController_DeleteRecord(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	uuid := record.GetUniqueID()

	err = controller.DeleteRecord(uuid)
	if err != nil {
		t.Fatalf("failed to delete record: %v", err)
	}
	testSleep()

	_, err = controller.GetRecordByLocator(locator)
	if err != ErrRecordDeleted {
		t.Errorf("expected ErrRecordDeleted, got: %v", err)
	}
}

func TestRecordController_DeleteAndIterate(t *testing.T) {
	controller := getTestRecordController(t)

	var uuids []string
	for i := 0; i < 5; i++ {
		locator := generateUniqueID()
		record, err := controller.CreateNewRecordWithLocator(locator)
		if err != nil {
			t.Fatalf("failed to create record %d: %v", i, err)
		}
		uuids = append(uuids, record.GetUniqueID())
		testSleep()
	}

	if err := controller.DeleteRecord(uuids[1]); err != nil {
		t.Fatalf("failed to delete record 1: %v", err)
	}
	testSleep()

	if err := controller.DeleteRecord(uuids[3]); err != nil {
		t.Fatalf("failed to delete record 3: %v", err)
	}
	testSleep()

	records, err := controller.IterateRecords(0, 10)
	if err != nil {
		t.Fatalf("failed to iterate records: %v", err)
	}

	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}

	foundUUIDs := make(map[string]bool)
	for _, rec := range records {
		foundUUIDs[rec.GetUniqueID()] = true
	}

	if foundUUIDs[uuids[1]] || foundUUIDs[uuids[3]] {
		t.Error("deleted records should not appear in iteration")
	}
}

func TestRecordController_TransactionOnDeletedRecord(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	uuid := record.GetUniqueID()

	err = controller.DeleteRecord(uuid)
	if err != nil {
		t.Fatalf("failed to delete record: %v", err)
	}
	testSleep()

	_, err = record.BeginTransaction()
	if err == nil {
		t.Error("expected error when beginning transaction on deleted record")
	}
}

func TestRecordController_IterateRecords(t *testing.T) {
	controller := getTestRecordController(t)

	for i := 0; i < 10; i++ {
		locator := generateUniqueID()
		_, err := controller.CreateNewRecordWithLocator(locator)
		if err != nil {
			t.Fatalf("failed to create record %d: %v", i, err)
		}
		testSleep()
	}

	page1, err := controller.IterateRecords(0, 5)
	if err != nil {
		t.Fatalf("failed to iterate page 1: %v", err)
	}

	if len(page1) != 5 {
		t.Errorf("expected 5 records in page 1, got %d", len(page1))
	}

	page2, err := controller.IterateRecords(5, 5)
	if err != nil {
		t.Fatalf("failed to iterate page 2: %v", err)
	}

	if len(page2) != 5 {
		t.Errorf("expected 5 records in page 2, got %d", len(page2))
	}
}

func TestRecordController_IterateEmpty(t *testing.T) {
	controller := getTestRecordController(t)

	records, err := controller.IterateRecords(0, 10)
	if err != nil {
		t.Fatalf("failed to iterate empty controller: %v", err)
	}

	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestRecordController_LocatorTooShort(t *testing.T) {
	controller := getTestRecordController(t)

	_, err := controller.CreateNewRecordWithLocator("abc")
	if err != ErrNewRecordLocatorTooShort {
		t.Errorf("expected ErrNewRecordLocatorTooShort, got: %v", err)
	}
}

func TestRecordController_LocatorTooLong(t *testing.T) {
	controller := getTestRecordController(t)

	longLocator := string(make([]byte, 257))
	for i := range longLocator {
		longLocator = string(append([]byte(longLocator[:i]), 'a'))
	}

	_, err := controller.CreateNewRecordWithLocator(longLocator[:257])
	if err != ErrNewRecordLocatorTooLong {
		t.Errorf("expected ErrNewRecordLocatorTooLong, got: %v", err)
	}
}

func TestRecordTransaction_SnapshotIsolation(t *testing.T) {
	controller := getTestRecordController(t)
	locator := generateUniqueID()

	record, err := controller.CreateNewRecordWithLocator(locator)
	if err != nil {
		t.Fatalf("failed to create record: %v", err)
	}
	testSleep()

	txn1, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}
	txn1.SetData([]byte("v1"))
	if err := txn1.Commit(); err != nil {
		t.Fatalf("failed to commit v1: %v", err)
	}
	testSleep()

	txnA, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction A: %v", err)
	}

	snapshotA := string(txnA.GetSnapshotData())
	if snapshotA != "v1" {
		t.Errorf("snapshot A should be v1, got: %s", snapshotA)
	}

	txnB, err := record.BeginTransaction()
	if err != nil {
		t.Fatalf("failed to begin transaction B: %v", err)
	}
	txnB.SetData([]byte("v2"))
	if err := txnB.Commit(); err != nil {
		t.Fatalf("failed to commit v2: %v", err)
	}
	testSleep()

	snapshotAAfter := string(txnA.GetSnapshotData())
	if snapshotAAfter != "v1" {
		t.Errorf("snapshot A should still be v1 after external change, got: %s", snapshotAAfter)
	}

	txnA.SetData([]byte("v3"))
	err = txnA.Commit()
	if err != ErrTransactionConflict {
		t.Errorf("expected ErrTransactionConflict, got: %v", err)
	}
}
