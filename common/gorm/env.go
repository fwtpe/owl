package gorm

import (
	"github.com/fwtpe/owl-backend/common/db"
	"github.com/fwtpe/owl-backend/common/utils"
	"github.com/jinzhu/gorm"
)

// This function converts the error to default database error
//
// See ToGormDbExt
var DefaultGormErrorConverter ErrorConverter = func(err error) error {
	if !utils.IsViable(err) {
		return nil
	}

	return db.NewDatabaseError(err)
}

// Converts gormDb to GormDbExt with conversion of DbError
func ToDefaultGormDbExt(gormDb *gorm.DB) *GormDbExt {
	gormDbExt := ToGormDbExt(gormDb)
	gormDbExt.ConvertError = DefaultGormErrorConverter
	return gormDbExt
}
