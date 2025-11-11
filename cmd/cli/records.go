package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/InsulaLabs/ferry/pkg/core"
	"github.com/InsulaLabs/ferry/pkg/datascape/records"
	"github.com/fatih/color"
)

func createRecordController(ctx context.Context, prefix string, f *core.Ferry) records.RecordController {
	cacheDuration := 5 * time.Minute
	cleanupInterval := 5 * time.Second
	cleanupJitter := 1 * time.Second

	rc := records.NewRecordController(
		ctx,
		prefix,
		cacheDuration,
		cleanupInterval,
		cleanupJitter,
		logger.WithGroup("records"),
		f.GetClient(),
	)

	rc.Start()
	return rc
}

func handleRecords(f *core.Ferry, args []string) {
	if len(args) < 1 {
		logger.Error("records: requires <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	subCommand := args[0]

	if subCommand == "list-groups" {
		handleRecordsListGroups(f, args[1:])
		return
	}

	if subCommand == "delete-group" {
		handleRecordsDeleteGroup(f, args[1:])
		return
	}

	if len(args) < 2 {
		logger.Error("records: requires <prefix> <sub-command> [args...]")
		printUsage()
		os.Exit(1)
	}

	prefix := args[0]
	subCommand = args[1]
	subArgs := args[2:]

	ctx := context.Background()
	rc := createRecordController(ctx, prefix, f)
	defer rc.Stop()

	switch subCommand {
	case "get":
		handleRecordsGet(rc, subArgs)
	case "create":
		handleRecordsCreate(rc, subArgs)
	case "delete":
		handleRecordsDelete(rc, subArgs)
	case "list":
		handleRecordsList(rc, subArgs)
	case "show":
		handleRecordsShow(rc, subArgs)
	case "set-data":
		handleRecordsSetData(rc, subArgs)
	case "add-locator":
		handleRecordsAddLocator(rc, subArgs)
	case "remove-locator":
		handleRecordsRemoveLocator(rc, subArgs)
	default:
		logger.Error("records: unknown sub-command", "sub_command", subCommand)
		printUsage()
		os.Exit(1)
	}
}

func handleRecordsGet(rc records.RecordController, args []string) {
	if len(args) != 1 {
		logger.Error("records get: requires <locator>")
		printUsage()
		os.Exit(1)
	}

	locator := args[0]

	record, err := rc.GetRecordByLocator(locator)
	if err != nil {
		if err == records.ErrRecordNotFound {
			fmt.Fprintf(os.Stderr, "%s Record not found with locator '%s'\n", color.RedString("Error:"), locator)
		} else if err == records.ErrRecordDeleted {
			fmt.Fprintf(os.Stderr, "%s Record has been deleted\n", color.RedString("Error:"))
		} else {
			logger.Error("Get record failed", "locator", locator, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	data := record.GetData(false)
	if record.HasError() {
		logger.Error("Get data failed", "error", record.GetError())
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), record.GetError())
		os.Exit(1)
	}

	fmt.Println()
	fmt.Fprintf(os.Stdout, "%s %s\n", color.CyanString("UUID:"), record.GetUniqueID())
	if len(data) == 0 {
		fmt.Fprintf(os.Stdout, "%s %s\n", color.CyanString("Data:"), color.YellowString("(empty)"))
	} else {
		fmt.Fprintf(os.Stdout, "%s %s\n", color.CyanString("Data:"), string(data))
	}
	fmt.Println()
}

func handleRecordsList(rc records.RecordController, args []string) {
	offset, limit := 0, 100

	var err error
	if len(args) > 0 {
		offset, err = strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid offset '%s': %v\n", color.RedString("Error:"), args[0], err)
			os.Exit(1)
		}
	}
	if len(args) > 1 {
		limit, err = strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s Invalid limit '%s': %v\n", color.RedString("Error:"), args[1], err)
			os.Exit(1)
		}
	}

	activeRecords, err := rc.IterateRecords(offset, limit)
	if err != nil {
		logger.Error("Iterate records failed", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	if len(activeRecords) == 0 {
		color.Yellow("No records found")
		return
	}

	fmt.Println()
	fmt.Fprintf(os.Stdout, "┌%s┬%s┐\n", strings.Repeat("─", 40), strings.Repeat("─", 42))
	fmt.Fprintf(os.Stdout, "│ %-38s │ %-40s │\n", "UUID", "Locators")
	fmt.Fprintf(os.Stdout, "├%s┼%s┤\n", strings.Repeat("─", 40), strings.Repeat("─", 42))

	for _, record := range activeRecords {
		locators := record.GetLocators()
		locatorStr := strings.Join(locators, ", ")
		if locatorStr == "" {
			locatorStr = "(no locators)"
		}
		fmt.Fprintf(os.Stdout, "│ %-38s │ %-40s │\n", record.GetUniqueID(), locatorStr)
	}
	fmt.Fprintf(os.Stdout, "└%s┴%s┘\n", strings.Repeat("─", 40), strings.Repeat("─", 42))
	fmt.Println()
}

func handleRecordsShow(rc records.RecordController, args []string) {
	if len(args) != 1 {
		logger.Error("records show: requires <locator>")
		printUsage()
		os.Exit(1)
	}

	locator := args[0]

	record, err := rc.GetRecordByLocator(locator)
	if err != nil {
		if err == records.ErrRecordNotFound {
			fmt.Fprintf(os.Stderr, "%s Record not found with locator '%s'\n", color.RedString("Error:"), locator)
		} else if err == records.ErrRecordDeleted {
			fmt.Fprintf(os.Stderr, "%s Record has been deleted\n", color.RedString("Error:"))
		} else {
			logger.Error("Get record failed", "locator", locator, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	fmt.Println()
	fmt.Fprintf(os.Stdout, "%s %s\n", color.CyanString("UUID:"), record.GetUniqueID())
	fmt.Fprintf(os.Stdout, "%s\n", strings.Repeat("─", 80))

	locators := record.GetLocators()
	fmt.Fprintf(os.Stdout, "%s\n", color.CyanString("Locators:"))
	if len(locators) == 0 {
		fmt.Fprintf(os.Stdout, "  %s\n", color.YellowString("(none)"))
	} else {
		for _, loc := range locators {
			fmt.Fprintf(os.Stdout, "  - %s\n", loc)
		}
	}
	fmt.Fprintf(os.Stdout, "%s\n", strings.Repeat("─", 80))

	data := record.GetData(false)
	if record.HasError() {
		logger.Error("Get data failed", "error", record.GetError())
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), record.GetError())
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, "%s\n", color.CyanString("Data:"))
	if len(data) == 0 {
		fmt.Fprintf(os.Stdout, "%s\n", color.YellowString("(empty)"))
	} else {
		fmt.Fprintf(os.Stdout, "%s\n", string(data))
	}
	fmt.Println()
}

func handleRecordsListGroups(f *core.Ferry, args []string) {
	ctx := context.Background()
	vc := core.GetValueController(f, "")

	prefixMap := make(map[string]bool)
	offset := 0
	limit := 1000
	recordPatterns := []string{":records:", ":locators:", ":tombstones:"}

	color.Cyan("Scanning for record groups...")

	for {
		keys, err := vc.IterateByPrefix(ctx, "*", offset, limit)
		if err != nil {
			logger.Error("Failed to iterate values", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}

		if len(keys) == 0 {
			break
		}

		for _, key := range keys {
			for _, pattern := range recordPatterns {
				if idx := strings.Index(key, pattern); idx > 0 {
					prefix := key[:idx]
					prefixMap[prefix] = true
					break
				}
			}
		}

		offset += limit
		time.Sleep(100 * time.Millisecond)
	}

	if len(prefixMap) == 0 {
		color.Yellow("No record groups found")
		return
	}

	prefixes := make([]string, 0, len(prefixMap))
	for prefix := range prefixMap {
		prefixes = append(prefixes, prefix)
	}

	for i := 0; i < len(prefixes); i++ {
		for j := i + 1; j < len(prefixes); j++ {
			if prefixes[i] > prefixes[j] {
				prefixes[i], prefixes[j] = prefixes[j], prefixes[i]
			}
		}
	}

	fmt.Println()
	fmt.Fprintf(os.Stdout, "┌%s┐\n", strings.Repeat("─", 62))
	fmt.Fprintf(os.Stdout, "│ %-60s │\n", "Record Groups")
	fmt.Fprintf(os.Stdout, "├%s┤\n", strings.Repeat("─", 62))
	for _, prefix := range prefixes {
		fmt.Fprintf(os.Stdout, "│ %-60s │\n", prefix)
	}
	fmt.Fprintf(os.Stdout, "├%s┤\n", strings.Repeat("─", 62))
	fmt.Fprintf(os.Stdout, "│ %-60s │\n", fmt.Sprintf("Total: %d", len(prefixes)))
	fmt.Fprintf(os.Stdout, "└%s┘\n", strings.Repeat("─", 62))
	fmt.Println()
}

func handleRecordsCreate(rc records.RecordController, args []string) {
	if len(args) < 1 || len(args) > 2 {
		logger.Error("records create: requires <locator> [data]")
		printUsage()
		os.Exit(1)
	}

	locator := args[0]
	var data string
	if len(args) == 2 {
		data = args[1]
	}

	record, err := rc.CreateNewRecordWithLocator(locator)
	if err != nil {
		if err == records.ErrNewRecordLocatorNotUnique {
			fmt.Fprintf(os.Stderr, "%s Locator '%s' already exists\n", color.RedString("Error:"), locator)
		} else if err == records.ErrNewRecordLocatorTooShort {
			fmt.Fprintf(os.Stderr, "%s Locator must be at least 4 characters\n", color.RedString("Error:"))
		} else if err == records.ErrNewRecordLocatorTooLong {
			fmt.Fprintf(os.Stderr, "%s Locator must be at most 256 characters\n", color.RedString("Error:"))
		} else {
			logger.Error("Create record failed", "locator", locator, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	if data != "" {
		tx, err := record.BeginTransaction()
		if err != nil {
			logger.Error("Begin transaction failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}

		tx.SetData([]byte(data))

		if err := tx.Commit(); err != nil {
			logger.Error("Commit transaction failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	}

	color.HiGreen("✓ Record created with UUID: %s", record.GetUniqueID())
}

func handleRecordsDelete(rc records.RecordController, args []string) {
	if len(args) != 1 {
		logger.Error("records delete: requires <uuid-or-locator>")
		printUsage()
		os.Exit(1)
	}

	identifier := args[0]

	record, err := rc.GetRecordByLocator(identifier)
	if err != nil {
		if err == records.ErrRecordNotFound {
			if err := rc.DeleteRecord(identifier); err != nil {
				logger.Error("Delete record failed", "uuid", identifier, "error", err)
				fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
				os.Exit(1)
			}
		} else if err == records.ErrRecordDeleted {
			fmt.Fprintf(os.Stderr, "%s Record has already been deleted\n", color.RedString("Error:"))
			os.Exit(1)
		} else {
			logger.Error("Get record failed", "identifier", identifier, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	} else {
		uuid := record.GetUniqueID()
		if err := rc.DeleteRecord(uuid); err != nil {
			logger.Error("Delete record failed", "uuid", uuid, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}
	}

	color.HiYellow("✓ Record marked for deletion (cleanup will happen in background)")
}

func handleRecordsSetData(rc records.RecordController, args []string) {
	if len(args) != 2 {
		logger.Error("records set-data: requires <locator> <data>")
		printUsage()
		os.Exit(1)
	}

	locator := args[0]
	data := args[1]

	record, err := rc.GetRecordByLocator(locator)
	if err != nil {
		if err == records.ErrRecordNotFound {
			fmt.Fprintf(os.Stderr, "%s Record not found with locator '%s'\n", color.RedString("Error:"), locator)
		} else if err == records.ErrRecordDeleted {
			fmt.Fprintf(os.Stderr, "%s Record has been deleted\n", color.RedString("Error:"))
		} else {
			logger.Error("Get record failed", "locator", locator, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	tx, err := record.BeginTransaction()
	if err != nil {
		logger.Error("Begin transaction failed", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	tx.SetData([]byte(data))

	if err := tx.Commit(); err != nil {
		if err == records.ErrTransactionConflict {
			fmt.Fprintf(os.Stderr, "%s Transaction conflict: data was modified concurrently. Please retry.\n", color.RedString("Error:"))
		} else if err == records.ErrTransactionAlreadyCommitted {
			fmt.Fprintf(os.Stderr, "%s Transaction already committed\n", color.RedString("Error:"))
		} else if err == records.ErrTransactionNoChanges {
			fmt.Fprintf(os.Stderr, "%s No changes to commit\n", color.RedString("Error:"))
		} else {
			logger.Error("Commit transaction failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	color.HiGreen("✓ Data updated successfully")
}

func getRecordByUUID(rc records.RecordController, uuid string) (records.ActiveRecord, error) {
	allRecords, err := rc.IterateRecords(0, 10000)
	if err != nil {
		return nil, err
	}

	for _, record := range allRecords {
		if record.GetUniqueID() == uuid {
			return record, nil
		}
	}

	return nil, records.ErrRecordNotFound
}

func handleRecordsAddLocator(rc records.RecordController, args []string) {
	if len(args) != 2 {
		logger.Error("records add-locator: requires <uuid> <locator>")
		printUsage()
		os.Exit(1)
	}

	uuid := args[0]
	locator := args[1]

	record, err := getRecordByUUID(rc, uuid)
	if err != nil {
		if err == records.ErrRecordNotFound {
			fmt.Fprintf(os.Stderr, "%s Record not found with UUID '%s'\n", color.RedString("Error:"), uuid)
		} else {
			logger.Error("Get record by UUID failed", "uuid", uuid, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	tx, err := record.BeginTransaction()
	if err != nil {
		logger.Error("Begin transaction failed", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	if err := tx.AddLocator(locator); err != nil {
		if err == records.ErrNewRecordLocatorTooShort {
			fmt.Fprintf(os.Stderr, "%s Locator must be at least 4 characters\n", color.RedString("Error:"))
		} else if err == records.ErrNewRecordLocatorTooLong {
			fmt.Fprintf(os.Stderr, "%s Locator must be at most 256 characters\n", color.RedString("Error:"))
		} else {
			logger.Error("Add locator failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	if err := tx.Commit(); err != nil {
		if err == records.ErrTransactionConflict {
			fmt.Fprintf(os.Stderr, "%s Transaction conflict: data was modified concurrently. Please retry.\n", color.RedString("Error:"))
		} else if err == records.ErrTransactionAlreadyCommitted {
			fmt.Fprintf(os.Stderr, "%s Transaction already committed\n", color.RedString("Error:"))
		} else if err == records.ErrTransactionNoChanges {
			fmt.Fprintf(os.Stderr, "%s No changes to commit\n", color.RedString("Error:"))
		} else {
			logger.Error("Commit transaction failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	color.HiGreen("✓ Locator added successfully")
}

func handleRecordsRemoveLocator(rc records.RecordController, args []string) {
	if len(args) != 2 {
		logger.Error("records remove-locator: requires <uuid> <locator>")
		printUsage()
		os.Exit(1)
	}

	uuid := args[0]
	locator := args[1]

	record, err := getRecordByUUID(rc, uuid)
	if err != nil {
		if err == records.ErrRecordNotFound {
			fmt.Fprintf(os.Stderr, "%s Record not found with UUID '%s'\n", color.RedString("Error:"), uuid)
		} else {
			logger.Error("Get record by UUID failed", "uuid", uuid, "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	tx, err := record.BeginTransaction()
	if err != nil {
		logger.Error("Begin transaction failed", "error", err)
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		os.Exit(1)
	}

	if err := tx.RemoveLocator(locator); err != nil {
		if err == records.ErrNewRecordLocatorTooShort {
			fmt.Fprintf(os.Stderr, "%s Locator must be at least 4 characters\n", color.RedString("Error:"))
		} else if err == records.ErrNewRecordLocatorTooLong {
			fmt.Fprintf(os.Stderr, "%s Locator must be at most 256 characters\n", color.RedString("Error:"))
		} else {
			logger.Error("Remove locator failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	if err := tx.Commit(); err != nil {
		if err == records.ErrTransactionConflict {
			fmt.Fprintf(os.Stderr, "%s Transaction conflict: data was modified concurrently. Please retry.\n", color.RedString("Error:"))
		} else if err == records.ErrTransactionAlreadyCommitted {
			fmt.Fprintf(os.Stderr, "%s Transaction already committed\n", color.RedString("Error:"))
		} else if err == records.ErrTransactionNoChanges {
			fmt.Fprintf(os.Stderr, "%s No changes to commit\n", color.RedString("Error:"))
		} else {
			logger.Error("Commit transaction failed", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
		}
		os.Exit(1)
	}

	color.HiGreen("✓ Locator removed successfully")
}

func handleRecordsDeleteGroup(f *core.Ferry, args []string) {
	if len(args) != 1 {
		logger.Error("records delete-group: requires <prefix>")
		printUsage()
		os.Exit(1)
	}

	prefix := args[0]
	ctx := context.Background()

	rc := createRecordController(ctx, prefix, f)
	defer rc.Stop()

	if !confirmFlag {
		records, err := rc.IterateRecords(0, 100)
		approxCount := 0
		if err == nil {
			approxCount = len(records)
		}

		reader := bufio.NewReader(os.Stdin)
		fmt.Fprintf(os.Stderr, "\n%s %s\n", color.RedString("⚠ WARNING:"),
			color.HiYellowString("You are about to delete ALL records in group '%s'", prefix))
		if approxCount > 0 {
			fmt.Fprintf(os.Stderr, "%s Found approximately %s to delete.\n",
				color.RedString("⚠"), color.HiRedString("%d records", approxCount))
		}
		fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("⚠"),
			color.YellowString("This operation cannot be undone!"))
		fmt.Fprintf(os.Stderr, "\nType '%s' to proceed: ", color.HiCyanString("yes"))

		input, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(input) != "yes" {
			color.Yellow("Cancelled")
			return
		}
	}

	color.Cyan("Deleting record group '%s'...", prefix)

	totalDeleted := 0
	failedRecords := 0

	for {
		records, err := rc.IterateRecords(0, 100)
		if err != nil {
			logger.Error("Failed to iterate records", "error", err)
			fmt.Fprintf(os.Stderr, "%s %s\n", color.RedString("Error:"), err)
			os.Exit(1)
		}

		if len(records) == 0 {
			break
		}

		for _, record := range records {
			uuid := record.GetUniqueID()
			if err := rc.DeleteRecord(uuid); err != nil {
				logger.Warn("Failed to delete record", "uuid", uuid, "error", err)
				failedRecords++
			} else {
				totalDeleted++
			}
		}

		if totalDeleted%100 == 0 && totalDeleted > 0 {
			color.Cyan("Marked %d records for deletion...", totalDeleted)
		}

		time.Sleep(250 * time.Millisecond)
	}

	if failedRecords > 0 {
		color.HiYellow("✓ Marked %d records for deletion (%d failed)", totalDeleted, failedRecords)
	} else {
		color.HiGreen("✓ Marked %d records for deletion", totalDeleted)
	}

	color.Cyan("Waiting for background cleanup to complete...")

	vc := core.GetValueController(f, "")
	tombstonePrefix := fmt.Sprintf("%s:tombstones:", prefix)

	waitCount := 0
	for {
		keys, err := vc.IterateByPrefix(ctx, tombstonePrefix, 0, 1000)
		if err != nil {
			if err == core.ErrKeyNotFound {
				break
			}
			logger.Warn("Failed to check tombstones", "error", err)
			break
		}

		if len(keys) == 0 {
			break
		}

		waitCount++
		if waitCount%3 == 0 {
			color.Cyan("Still waiting... %d tombstones remaining", len(keys))
		}

		time.Sleep(2 * time.Second)
	}

	color.HiGreen("✓ Cleanup complete - all records and associated keys removed")
}
