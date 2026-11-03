package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type Logger struct{ DB *sql.DB }

func (l Logger) Record(ctx context.Context, actor, action, targetType, targetID string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	b, _ := json.Marshal(metadata)
	_, err := l.DB.ExecContext(ctx, `INSERT INTO audit_log(actor,action,target_type,target_id,metadata,created_at) VALUES(?,?,?,?,?,?)`, actor, action, targetType, targetID, string(b), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
