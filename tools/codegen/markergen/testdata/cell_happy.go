// Package testdata holds fixture Go files for collect_test.go.
// This file is NOT compiled by the regular build — it is read as raw source
// by parser.ParseFile in tests.
package testcell

import "context"

// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
// +cell:listener:ref=cell.InternalListener,prefix=/internal/v1
type OrderCell struct {
	// +slice:route:slice=ordercreate,subPath=/orders
	CreateHandler interface{}

	// +slice:subscribe:slice=configsubscribe,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore
	ConfigSub interface{}

	// not a marker — regular comment
	OtherField interface{}
}

// unrelated type — no markers
type helperStruct struct{}

// ensure context import is used (avoids unused import in fixture)
var _ context.Context
