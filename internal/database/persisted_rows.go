package database

import (
	"fmt"
	"reflect"

	"gorm.io/gorm"
)

// InsertPreservingValues copies persisted model rows without applying creation
// hooks or defaults. Struct CreateInBatches treats false, 0 and empty strings as
// missing values, which would reactivate disabled accounts during migration or
// restore. Column maps preserve the exact values already stored in the source.
func InsertPreservingValues(db *gorm.DB, rows any, batchSize int) error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(rows); err != nil {
		return err
	}
	slice := reflect.Indirect(reflect.ValueOf(rows))
	if slice.Kind() != reflect.Slice {
		return fmt.Errorf("persisted rows must be a slice, got %T", rows)
	}
	if len(stmt.Schema.DBNames) == 0 {
		return fmt.Errorf("persisted rows have no database columns")
	}
	values := make([]map[string]any, 0, slice.Len())
	for i := 0; i < slice.Len(); i++ {
		row := make(map[string]any, len(stmt.Schema.DBNames))
		for _, name := range stmt.Schema.DBNames {
			field := stmt.Schema.FieldsByDBName[name]
			if field.Creatable {
				row[name], _ = field.ValueOf(db.Statement.Context, slice.Index(i))
			}
		}
		values = append(values, row)
	}
	if len(values) == 0 {
		return nil
	}
	// Bound batches by each engine's bind-parameter limit.
	parameterLimit := 65535
	if isSQLite(db) {
		parameterLimit = 32766
	}
	maxBatch := parameterLimit / len(stmt.Schema.DBNames)
	if batchSize <= 0 || batchSize > maxBatch {
		batchSize = maxBatch
	}
	return db.Table(stmt.Schema.Table).CreateInBatches(values, batchSize).Error
}
