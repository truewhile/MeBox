package service

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/truewhile/MeBox/internal/database"
	"github.com/truewhile/MeBox/internal/model"
)

// PostgreSQL 逻辑备份 / 恢复。
//
// SQLite 的备份是「VACUUM INTO 出一个一致的库文件，恢复时替换文件」，而
// PostgreSQL 既没有 VACUUM INTO，数据也由服务端进程管理，没有可替换的单文件，
// 所以这里实现逻辑备份：把每张表按主键顺序分块序列化成 JSON Lines（gzip 压缩）
// 写入 {data_dir}/backups/。
//
// 分块序列化而不是整表 marshal：media 表在真实媒体库上可达几十万行、每行还带
// overview / path 等长文本，整表进内存会把小内存的 NAS 打爆。
//
// 恢复在单个事务内「先清空全部模型表、再逐块插入」：任何一步失败（包括备份文件
// 读到一半损坏）都整体回滚，不会留下半清空的库。因此恢复不需要重启服务。
const (
	postgresBackupSuffix   = ".json.gz"
	postgresBackupFormat   = "mebox-postgres-logical"
	postgresBackupVersion  = 1
	backupDumpChunkSize    = 1000
	backupRestoreBatchSize = 500
)

type postgresBackupHeader struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// postgresBackupRow 是「列名 → JSON 值」的一行。
//
// 刻意逐列反射序列化，而不是直接 json.Marshal 模型结构体：模型里有大量带
// json:"-" 的列（Base.DeletedAt、User.AllowedLibraryIDs、APIConfig.APIKey、
// StrmAccount.Config、PlayProfile.PINHash、ReaderBookSource.RawJSON…），走结构体
// 自带的序列化会把这些列整列丢掉——软删除行会在恢复后「复活」，加密后的第三方
// 密钥与 PIN 会全部丢失。
type postgresBackupRow map[string]json.RawMessage

type postgresBackupChunk struct {
	Table string              `json:"table"`
	Rows  []postgresBackupRow `json:"rows"`
}

// parseModelSchema 返回模型对应的表名与 GORM schema（逐列反射所需）。
func parseModelSchema(db *gorm.DB, m any) (table string, sch *schema.Schema, err error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(m); err != nil {
		return "", nil, err
	}
	return stmt.Schema.Table, stmt.Schema, nil
}

// postgresBackupModelSchemas 返回「表名 → 模型 schema / 结构体类型」，用于把备份
// 里的行还原成 GORM 能插入的具体类型。
func postgresBackupModelSchemas(db *gorm.DB) (map[string]*schema.Schema, map[string]reflect.Type, error) {
	schemas := make(map[string]*schema.Schema, len(model.AllModels()))
	types := make(map[string]reflect.Type, len(model.AllModels()))
	for _, m := range model.AllModels() {
		table, sch, err := parseModelSchema(db, m)
		if err != nil {
			return nil, nil, err
		}
		schemas[table] = sch
		types[table] = reflect.TypeOf(m).Elem()
	}
	return schemas, types, nil
}

// createPostgresBackup 把全库导出成一个 gzip 压缩的 JSON Lines 文件。
func (b *BackupService) createPostgresBackup(ctx context.Context, stamp string) (string, error) {
	dst := filepath.Join(b.backupDir(), "mebox_"+stamp+postgresBackupSuffix)
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			// 半截备份文件比没有备份更危险：留着会让人以为有可用的还原点。
			_ = os.Remove(dst)
		}
	}()

	zw := gzip.NewWriter(file)
	enc := json.NewEncoder(zw)
	if err := enc.Encode(postgresBackupHeader{
		Format:    postgresBackupFormat,
		Version:   postgresBackupVersion,
		CreatedAt: time.Now(),
	}); err != nil {
		return "", fmt.Errorf("写入备份头失败: %w", err)
	}
	for _, m := range model.AllModels() {
		table, sch, err := parseModelSchema(b.db, m)
		if err != nil {
			return "", err
		}
		if !b.db.Migrator().HasTable(table) {
			continue
		}
		if err := b.dumpPostgresTable(ctx, m, sch, table, enc); err != nil {
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("压缩备份失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("写入备份失败: %w", err)
	}
	complete = true
	return dst, nil
}

// dumpPostgresTable 按主键分块导出一张表。FindInBatches 会自己按主键升序游标
// 翻页（WHERE pk > last），不会像 OFFSET 分页那样在大表上做 O(n²) 扫描。
func (b *BackupService) dumpPostgresTable(ctx context.Context, m any, sch *schema.Schema, table string, enc *json.Encoder) error {
	// 软删除行也要备份：恢复后应还原成删除前的样子，而不是让它们「复活」。
	slicePtr := reflect.New(reflect.SliceOf(reflect.TypeOf(m).Elem()))
	err := b.db.WithContext(ctx).Unscoped().
		FindInBatches(slicePtr.Interface(), backupDumpChunkSize, func(_ *gorm.DB, _ int) error {
			rows, err := encodePostgresBackupRows(ctx, sch, slicePtr.Elem())
			if err != nil {
				return fmt.Errorf("编码表 %s 失败: %w", table, err)
			}
			return enc.Encode(postgresBackupChunk{Table: table, Rows: rows})
		}).Error
	if err != nil {
		return fmt.Errorf("导出表 %s 失败: %w", table, err)
	}
	return nil
}

// backupPersistableFields 返回真正对应数据库列的字段。
//
// GORM 的 Schema.Fields 里还带着 gorm:"-" 的字段（例如 User.AllowedLibraryList、
// LibraryTagList），它们的 DBName 是空字符串。不过滤的话多个「列」会共用空键：
// 编码时互相覆盖，解码时把某个字段的值写到另一个字段上，报出
// 「cannot unmarshal number into Go value of type []string」这类与真实数据无关的错。
func backupPersistableFields(sch *schema.Schema) []*schema.Field {
	out := make([]*schema.Field, 0, len(sch.Fields))
	for _, field := range sch.Fields {
		if field.DBName == "" {
			continue
		}
		out = append(out, field)
	}
	return out
}

// encodePostgresBackupRows 逐列反射序列化一个批次。
func encodePostgresBackupRows(ctx context.Context, sch *schema.Schema, slice reflect.Value) ([]postgresBackupRow, error) {
	fields := backupPersistableFields(sch)
	rows := make([]postgresBackupRow, 0, slice.Len())
	for i := 0; i < slice.Len(); i++ {
		element := slice.Index(i)
		row := make(postgresBackupRow, len(fields))
		for _, field := range fields {
			value := field.ReflectValueOf(ctx, element)
			if !value.IsValid() {
				continue
			}
			raw, err := json.Marshal(value.Interface())
			if err != nil {
				return nil, fmt.Errorf("列 %s: %w", field.DBName, err)
			}
			row[field.DBName] = raw
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// decodePostgresBackupRows 把「列名 → JSON 值」还原成可直接插入的具体切片。
func decodePostgresBackupRows(ctx context.Context, sch *schema.Schema, modelType reflect.Type, rows []postgresBackupRow) (any, error) {
	fields := backupPersistableFields(sch)
	slice := reflect.MakeSlice(reflect.SliceOf(modelType), len(rows), len(rows))
	for i, row := range rows {
		element := slice.Index(i)
		for _, field := range fields {
			raw, ok := row[field.DBName]
			if !ok {
				continue
			}
			holder := reflect.New(field.FieldType)
			if err := json.Unmarshal(raw, holder.Interface()); err != nil {
				return nil, fmt.Errorf("列 %s: %w", field.DBName, err)
			}
			if err := field.Set(ctx, element, holder.Elem().Interface()); err != nil {
				return nil, fmt.Errorf("列 %s: %w", field.DBName, err)
			}
		}
	}
	return slice.Interface(), nil
}

// restorePostgresBackup 用逻辑备份重建全库。
func (b *BackupService) restorePostgresBackup(ctx context.Context, src string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("备份文件不是有效的 gzip: %w", err)
	}
	defer zr.Close()

	dec := json.NewDecoder(zr)
	var header postgresBackupHeader
	if err := dec.Decode(&header); err != nil {
		return fmt.Errorf("备份头损坏: %w", err)
	}
	if header.Format != postgresBackupFormat || header.Version != postgresBackupVersion {
		return fmt.Errorf("不支持的备份格式 %q (v%d)", header.Format, header.Version)
	}
	schemas, modelTypes, err := postgresBackupModelSchemas(b.db)
	if err != nil {
		return err
	}

	// 恢复必须在单个事务里完成：清空成功后中途失败若不回滚，库就空了。
	return b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 逆序清空，保证 library_roots → libraries 这类外键先删子表。
		models := model.AllModels()
		for i := len(models) - 1; i >= 0; i-- {
			m := models[i]
			if !tx.Migrator().HasTable(m) {
				continue
			}
			if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(m).Error; err != nil {
				return fmt.Errorf("清空表失败: %w", err)
			}
		}
		for {
			var chunk postgresBackupChunk
			decodeErr := dec.Decode(&chunk)
			if errors.Is(decodeErr, io.EOF) {
				return nil
			}
			if decodeErr != nil {
				return fmt.Errorf("备份文件损坏: %w", decodeErr)
			}
			sch, ok := schemas[chunk.Table]
			if !ok {
				return fmt.Errorf("备份包含未知表 %s，可能来自不兼容的版本", chunk.Table)
			}
			if len(chunk.Rows) == 0 {
				continue
			}
			rows, err := decodePostgresBackupRows(ctx, sch, modelTypes[chunk.Table], chunk.Rows)
			if err != nil {
				return fmt.Errorf("表 %s 解码失败: %w", chunk.Table, err)
			}
			if err := database.InsertPreservingValues(tx.Unscoped(), rows, backupRestoreBatchSize); err != nil {
				return fmt.Errorf("恢复表 %s 失败: %w", chunk.Table, err)
			}
		}
	})
}
