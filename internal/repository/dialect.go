package repository

import "gorm.io/gorm"

// LikeOperator 返回当前方言下「与 SQLite LIKE 行为一致」的模式匹配运算符。
//
// SQLite 的 LIKE 对 ASCII 不区分大小写（默认编译选项），PostgreSQL 的 LIKE 区分
// 大小写、只有 ILIKE 才不区分。同一个搜索词在两种方言下必须给出同样的结果集，
// 否则换库之后「inception」就再也搜不到「Inception」。
//
// 供构造原始 SQL 片段的调用方（含 service 层）使用。
func LikeOperator(db *gorm.DB) string {
	return likeOperator(db)
}

func likeOperator(db *gorm.DB) string {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return "ILIKE"
	}
	return "LIKE"
}
