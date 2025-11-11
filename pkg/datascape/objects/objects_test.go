package objects

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/InsulaLabs/ferry/pkg/datascape/records"

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

func getTestSchema() ObjectSchema {
	return ObjectSchema{
		UniqueLocatorCategories:         []string{"email", "username", "phone"},
		BundledLocatorCategories:        []string{"tag", "category"},
		ExternalRecordLocatorCategories: []string{"user_profile", "user_settings", "user_preferences"},
	}
}

func getTestObjectController(t *testing.T) *ObjectController {
	ctx := context.Background()
	client := getTestClient(t)
	logger := getTestLogger()
	typename := generateUniqueID()
	schema := getTestSchema()

	controller := NewObjectController(
		ctx,
		typename,
		schema,
		1*time.Second,
		10*time.Second,
		5*time.Second,
		logger, client,
	)
	controller.Start()
	t.Cleanup(func() {
		controller.Stop()
	})

	return controller
}

func TestObjectController_CreateObject(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()
	initialData := []byte(`{"test": "data"}`)

	obj, err := controller.CreateObject(locator, initialData)
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	if obj.Version != 1 {
		t.Errorf("expected version 1, got %d", obj.Version)
	}

	if string(obj.Data) != string(initialData) {
		t.Errorf("data mismatch: got %s, want %s", string(obj.Data), string(initialData))
	}

	retrieved, err := controller.GetObjectByLocator(locator)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}

	if string(retrieved.Data) != string(initialData) {
		t.Errorf("retrieved data mismatch: got %s, want %s", string(retrieved.Data), string(initialData))
	}
}

func TestObjectController_CreateDuplicate(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data1"))
	if err != nil {
		t.Fatalf("failed to create first object: %v", err)
	}
	testSleep()

	_, err = controller.CreateObject(locator, []byte("data2"))
	if err != ErrLocatorAlreadyExists {
		t.Errorf("expected ErrLocatorAlreadyExists, got: %v", err)
	}
}

func TestObjectController_GetNonExistent(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.GetObjectByLocator(locator)
	if err != ErrObjectNotFound {
		t.Errorf("expected ErrObjectNotFound, got: %v", err)
	}
}

func TestObjectController_UpdateObjectData(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("initial"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.UpdateObjectData(locator, []byte("updated"))
	if err != nil {
		t.Fatalf("failed to update object data: %v", err)
	}
	testSleep()

	obj, err := controller.GetObjectByLocator(locator)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}

	if string(obj.Data) != "updated" {
		t.Errorf("data mismatch: got %s, want updated", string(obj.Data))
	}
}

func TestObjectController_UpdateObjectDataConcurrent(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("initial"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	done := make(chan error, 2)

	go func() {
		err := controller.UpdateObjectData(locator, []byte("update-1"))
		done <- err
	}()

	go func() {
		time.Sleep(50 * time.Millisecond)
		err := controller.UpdateObjectData(locator, []byte("update-2"))
		done <- err
	}()

	err1 := <-done
	err2 := <-done

	if err1 != nil && err2 != nil {
		t.Fatalf("both updates failed: %v, %v", err1, err2)
	}

	testSleep()

	obj, err := controller.GetObjectByLocator(locator)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}

	data := string(obj.Data)
	if data != "update-1" && data != "update-2" {
		t.Errorf("unexpected data: %s", data)
	}
}

func TestObjectTransaction_UpdateData(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("initial"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	txn, err := controller.BeginTransaction(locator)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	txn.UpdateData([]byte("via-transaction"))

	err = txn.Commit()
	if err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	obj, err := controller.GetObjectByLocator(locator)
	if err != nil {
		t.Fatalf("failed to get object: %v", err)
	}

	if string(obj.Data) != "via-transaction" {
		t.Errorf("data mismatch: got %s, want via-transaction", string(obj.Data))
	}
}

func TestObjectTransaction_MultipleOperations(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("{}"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	txn, err := controller.BeginTransaction(locator)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	txn.UpdateData([]byte("new-data"))
	if err := txn.AddUniqueLocator("email", "test@example.com"); err != nil {
		t.Fatalf("failed to add unique locator: %v", err)
	}
	if err := txn.AddBundledLocator("tag", "golang"); err != nil {
		t.Fatalf("failed to add bundled locator: %v", err)
	}
	if err := txn.SetExternalRecordLocator("user_profile", "profile-uuid-123"); err != nil {
		t.Fatalf("failed to set external locator: %v", err)
	}

	err = txn.Commit()
	if err != nil {
		t.Fatalf("failed to commit transaction: %v", err)
	}
	testSleep()

	obj, err := controller.GetObjectByLocator(locator)
	if err != nil {
		t.Fatalf("failed to get object by primary locator: %v", err)
	}
	if string(obj.Data) != "new-data" {
		t.Errorf("data not updated")
	}

	objByEmail, err := controller.GetObjectByLocator("email:test@example.com")
	if err != nil {
		t.Fatalf("failed to get object by email locator: %v", err)
	}
	if string(objByEmail.Data) != "new-data" {
		t.Error("email locator doesn't resolve to same object")
	}

	objByTag, err := controller.GetObjectByLocator("tag:golang")
	if err != nil {
		t.Fatalf("failed to get object by tag locator: %v", err)
	}
	if string(objByTag.Data) != "new-data" {
		t.Error("tag locator doesn't resolve to same object")
	}

	externalValue, err := controller.GetExternalRecordLocator(locator, "user_profile")
	if err != nil {
		t.Fatalf("failed to get external locator: %v", err)
	}
	if externalValue != "profile-uuid-123" {
		t.Errorf("external locator mismatch: got %s, want profile-uuid-123", externalValue)
	}
}

func TestObjectController_AddUniqueLocator(t *testing.T) {
	controller := getTestObjectController(t)
	primaryLocator := generateUniqueID()

	_, err := controller.CreateObject(primaryLocator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.AddUniqueLocator(primaryLocator, "email", "user@example.com")
	if err != nil {
		t.Fatalf("failed to add unique locator: %v", err)
	}
	testSleep()

	objByEmail, err := controller.GetObjectByLocator("email:user@example.com")
	if err != nil {
		t.Fatalf("failed to get object by email: %v", err)
	}

	objByPrimary, err := controller.GetObjectByLocator(primaryLocator)
	if err != nil {
		t.Fatalf("failed to get object by primary: %v", err)
	}

	if string(objByEmail.Data) != string(objByPrimary.Data) {
		t.Error("locators should resolve to same object")
	}
}

func TestObjectController_RemoveUniqueLocator(t *testing.T) {
	controller := getTestObjectController(t)
	primaryLocator := generateUniqueID()

	_, err := controller.CreateObject(primaryLocator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.AddUniqueLocator(primaryLocator, "email", "remove@example.com")
	if err != nil {
		t.Fatalf("failed to add unique locator: %v", err)
	}
	testSleep()

	err = controller.RemoveUniqueLocator(primaryLocator, "email", "remove@example.com")
	if err != nil {
		t.Fatalf("failed to remove unique locator: %v", err)
	}
	testSleep()

	_, err = controller.GetObjectByLocator("email:remove@example.com")
	if err != ErrObjectNotFound {
		t.Errorf("removed locator should not work, got: %v", err)
	}

	_, err = controller.GetObjectByLocator(primaryLocator)
	if err != nil {
		t.Errorf("primary locator should still work: %v", err)
	}
}

func TestObjectController_UpdateUniqueLocator(t *testing.T) {
	controller := getTestObjectController(t)
	primaryLocator := generateUniqueID()

	_, err := controller.CreateObject(primaryLocator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.AddUniqueLocator(primaryLocator, "email", "old@example.com")
	if err != nil {
		t.Fatalf("failed to add initial email: %v", err)
	}
	testSleep()

	err = controller.UpdateUniqueLocator(primaryLocator, "email", "old@example.com", "new@example.com")
	if err != nil {
		t.Fatalf("failed to update unique locator: %v", err)
	}
	testSleep()

	_, err = controller.GetObjectByLocator("email:old@example.com")
	if err != ErrObjectNotFound {
		t.Errorf("old email should not work, got: %v", err)
	}

	_, err = controller.GetObjectByLocator("email:new@example.com")
	if err != nil {
		t.Errorf("new email should work: %v", err)
	}
}

func TestObjectController_UniqueLocatorConflict(t *testing.T) {
	controller := getTestObjectController(t)
	locatorA := generateUniqueID()
	locatorB := generateUniqueID()
	conflictEmail := "conflict@example.com"

	_, err := controller.CreateObject(locatorA, []byte("A"))
	if err != nil {
		t.Fatalf("failed to create object A: %v", err)
	}
	testSleep()

	err = controller.AddUniqueLocator(locatorA, "email", conflictEmail)
	if err != nil {
		t.Fatalf("failed to add email to A: %v", err)
	}
	testSleep()

	_, err = controller.CreateObject(locatorB, []byte("B"))
	if err != nil {
		t.Fatalf("failed to create object B: %v", err)
	}
	testSleep()

	err = controller.AddUniqueLocator(locatorB, "email", conflictEmail)
	if err != ErrLocatorAlreadyExists {
		t.Errorf("expected ErrLocatorAlreadyExists, got: %v", err)
	}
}

func TestObjectController_AddBundledLocator(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.AddBundledLocator(locator, "tag", "golang")
	if err != nil {
		t.Fatalf("failed to add tag golang: %v", err)
	}
	testSleep()

	err = controller.AddBundledLocator(locator, "tag", "testing")
	if err != nil {
		t.Fatalf("failed to add tag testing: %v", err)
	}
	testSleep()

	_, err = controller.GetObjectByLocator("tag:golang")
	if err != nil {
		t.Errorf("tag:golang should work: %v", err)
	}

	_, err = controller.GetObjectByLocator("tag:testing")
	if err != nil {
		t.Errorf("tag:testing should work: %v", err)
	}
}

func TestObjectController_RemoveBundledLocator(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.AddBundledLocator(locator, "tag", "tag1")
	if err != nil {
		t.Fatalf("failed to add tag1: %v", err)
	}
	testSleep()

	err = controller.AddBundledLocator(locator, "tag", "tag2")
	if err != nil {
		t.Fatalf("failed to add tag2: %v", err)
	}
	testSleep()

	err = controller.RemoveBundledLocator(locator, "tag", "tag1")
	if err != nil {
		t.Fatalf("failed to remove tag1: %v", err)
	}
	testSleep()

	_, err = controller.GetObjectByLocator("tag:tag1")
	if err != ErrObjectNotFound {
		t.Errorf("tag1 should be removed, got: %v", err)
	}

	_, err = controller.GetObjectByLocator("tag:tag2")
	if err != nil {
		t.Errorf("tag2 should still work: %v", err)
	}
}

func TestObjectController_SetExternalRecordLocator(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_profile", "profile-uuid-123")
	if err != nil {
		t.Fatalf("failed to set external locator: %v", err)
	}
	testSleep()

	value, err := controller.GetExternalRecordLocator(locator, "user_profile")
	if err != nil {
		t.Fatalf("failed to get external locator: %v", err)
	}

	if value != "profile-uuid-123" {
		t.Errorf("external locator mismatch: got %s, want profile-uuid-123", value)
	}
}

func TestObjectController_UpdateExternalRecordLocator(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_profile", "old-value")
	if err != nil {
		t.Fatalf("failed to set initial external locator: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_profile", "new-value")
	if err != nil {
		t.Fatalf("failed to update external locator: %v", err)
	}
	testSleep()

	value, err := controller.GetExternalRecordLocator(locator, "user_profile")
	if err != nil {
		t.Fatalf("failed to get external locator: %v", err)
	}

	if value != "new-value" {
		t.Errorf("external locator should be updated: got %s, want new-value", value)
	}
}

func TestObjectController_RemoveExternalRecordLocator(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_profile", "some-value")
	if err != nil {
		t.Fatalf("failed to set external locator: %v", err)
	}
	testSleep()

	err = controller.RemoveExternalRecordLocator(locator, "user_profile")
	if err != nil {
		t.Fatalf("failed to remove external locator: %v", err)
	}
	testSleep()

	_, err = controller.GetExternalRecordLocator(locator, "user_profile")
	if err != ErrLocatorNotFound {
		t.Errorf("expected ErrLocatorNotFound, got: %v", err)
	}
}

func TestObjectController_MultipleExternalLocators(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_profile", "profile-123")
	if err != nil {
		t.Fatalf("failed to set user_profile: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_settings", "settings-456")
	if err != nil {
		t.Fatalf("failed to set user_settings: %v", err)
	}
	testSleep()

	err = controller.SetExternalRecordLocator(locator, "user_preferences", "prefs-789")
	if err != nil {
		t.Fatalf("failed to set user_preferences: %v", err)
	}
	testSleep()

	profile, err := controller.GetExternalRecordLocator(locator, "user_profile")
	if err != nil || profile != "profile-123" {
		t.Errorf("user_profile mismatch: got %s, err %v", profile, err)
	}

	settings, err := controller.GetExternalRecordLocator(locator, "user_settings")
	if err != nil || settings != "settings-456" {
		t.Errorf("user_settings mismatch: got %s, err %v", settings, err)
	}

	prefs, err := controller.GetExternalRecordLocator(locator, "user_preferences")
	if err != nil || prefs != "prefs-789" {
		t.Errorf("user_preferences mismatch: got %s, err %v", prefs, err)
	}

	err = controller.RemoveExternalRecordLocator(locator, "user_settings")
	if err != nil {
		t.Fatalf("failed to remove user_settings: %v", err)
	}
	testSleep()

	_, err = controller.GetExternalRecordLocator(locator, "user_settings")
	if err != ErrLocatorNotFound {
		t.Error("user_settings should be removed")
	}

	profile2, err := controller.GetExternalRecordLocator(locator, "user_profile")
	if err != nil || profile2 != "profile-123" {
		t.Error("user_profile should still exist")
	}

	prefs2, err := controller.GetExternalRecordLocator(locator, "user_preferences")
	if err != nil || prefs2 != "prefs-789" {
		t.Error("user_preferences should still exist")
	}
}

func TestObjectController_DeleteObject(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.DeleteObject(locator)
	if err != nil {
		t.Fatalf("failed to delete object: %v", err)
	}
	testSleep()

	_, err = controller.GetObjectByLocator(locator)
	if err != ErrObjectNotFound {
		t.Errorf("expected ErrObjectNotFound after delete, got: %v", err)
	}
}

func TestObjectController_DeleteByAnyLocator(t *testing.T) {
	controller := getTestObjectController(t)
	primaryLocator := generateUniqueID()
	secondaryLocator := generateUniqueID()

	_, err := controller.CreateObject(primaryLocator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	txn, err := controller.BeginTransaction(primaryLocator)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	txn.AddUniqueLocator("email", secondaryLocator)
	if err := txn.Commit(); err != nil {
		t.Fatalf("failed to add secondary locator: %v", err)
	}
	testSleep()

	err = controller.DeleteObject("email:" + secondaryLocator)
	if err != nil {
		t.Fatalf("failed to delete by secondary locator: %v", err)
	}
	testSleep()

	_, err = controller.GetObjectByLocator(primaryLocator)
	if err != ErrObjectNotFound && err != records.ErrRecordDeleted {
		t.Errorf("primary locator should not work after delete, got: %v", err)
	}

	_, err = controller.GetObjectByLocator("email:" + secondaryLocator)
	if err != ErrObjectNotFound && err != records.ErrRecordDeleted {
		t.Errorf("secondary locator should not work after delete, got: %v", err)
	}
}

func TestObjectController_IterateObjects(t *testing.T) {
	controller := getTestObjectController(t)

	for i := 0; i < 10; i++ {
		locator := generateUniqueID()
		_, err := controller.CreateObject(locator, []byte(fmt.Sprintf("data-%d", i)))
		if err != nil {
			t.Fatalf("failed to create object %d: %v", i, err)
		}
		testSleep()
	}

	page1, err := controller.IterateObjects(0, 5)
	if err != nil {
		t.Fatalf("failed to iterate page 1: %v", err)
	}

	if len(page1) != 5 {
		t.Errorf("expected 5 objects in page 1, got %d", len(page1))
	}

	page2, err := controller.IterateObjects(5, 5)
	if err != nil {
		t.Fatalf("failed to iterate page 2: %v", err)
	}

	if len(page2) != 5 {
		t.Errorf("expected 5 objects in page 2, got %d", len(page2))
	}
}

func TestObjectController_InvalidCategory(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	txn, err := controller.BeginTransaction(locator)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	err = txn.AddUniqueLocator("invalid_category", "value")
	if err != ErrCategoryNotInSchema {
		t.Errorf("expected ErrCategoryNotInSchema, got: %v", err)
	}
}

func TestObjectController_WrongCategoryType(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("data"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	err = controller.AddBundledLocator(locator, "email", "value")
	if err != ErrCategoryNotInSchema {
		t.Errorf("expected ErrCategoryNotInSchema when using unique category as bundled, got: %v", err)
	}
}

func TestObjectTransaction_Conflict(t *testing.T) {
	controller := getTestObjectController(t)
	locator := generateUniqueID()

	_, err := controller.CreateObject(locator, []byte("initial"))
	if err != nil {
		t.Fatalf("failed to create object: %v", err)
	}
	testSleep()

	txn1, err := controller.BeginTransaction(locator)
	if err != nil {
		t.Fatalf("failed to begin transaction 1: %v", err)
	}

	err = controller.UpdateObjectData(locator, []byte("external-update"))
	if err != nil {
		t.Fatalf("failed to do external update: %v", err)
	}
	testSleep()

	txn1.UpdateData([]byte("conflicting-update"))
	err = txn1.Commit()
	if err == nil {
		t.Error("expected conflict error")
	}
}
