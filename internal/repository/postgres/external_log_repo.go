package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	externallog "github.com/arturo/autohost-cloud-api/internal/domain/external_log"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type ExternalLogRepository struct {
	db *sqlx.DB
}

func NewExternalLogRepository(db *sqlx.DB) *ExternalLogRepository {
	return &ExternalLogRepository{db: db}
}

func (r *ExternalLogRepository) CreateToken(ctx context.Context, userID, name, tokenHash, tokenPrefix string, nodeID *string) (*externallog.ExternalLogToken, error) {
	var token externallog.ExternalLogToken
	query := `
		INSERT INTO external_log_tokens (user_id, name, token_hash, token_prefix, node_id)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, name, token_hash, token_prefix, node_id, created_at, last_seen_at, revoked_at`

	err := r.db.QueryRowxContext(ctx, query, userID, name, tokenHash, tokenPrefix, nodeID).StructScan(&token)
	if err != nil {
		return nil, fmt.Errorf("failed to create external log token: %w", err)
	}
	return &token, nil
}

func (r *ExternalLogRepository) GetTokenByHash(ctx context.Context, tokenHash string) (*externallog.ExternalLogToken, error) {
	var token externallog.ExternalLogToken
	query := `
		SELECT id, user_id, name, token_hash, token_prefix, node_id, created_at, last_seen_at, revoked_at
		FROM external_log_tokens
		WHERE token_hash = $1`

	err := r.db.GetContext(ctx, &token, query, tokenHash)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *ExternalLogRepository) UpdateTokenLastSeen(ctx context.Context, tokenID string) error {
	query := `UPDATE external_log_tokens SET last_seen_at = NOW() WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, tokenID)
	return err
}

func (r *ExternalLogRepository) ListTokensByUserID(ctx context.Context, userID string) ([]*externallog.ExternalLogToken, error) {
	var tokens []*externallog.ExternalLogToken
	query := `
		SELECT id, user_id, name, token_hash, token_prefix, node_id, created_at, last_seen_at, revoked_at
		FROM external_log_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC`

	err := r.db.SelectContext(ctx, &tokens, query, userID)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		tokens = []*externallog.ExternalLogToken{}
	}
	return tokens, nil
}

func (r *ExternalLogRepository) RevokeToken(ctx context.Context, userID, tokenID string) error {
	query := `UPDATE external_log_tokens SET revoked_at = NOW() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`
	res, err := r.db.ExecContext(ctx, query, tokenID, userID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err == nil && rows == 0 {
		return fmt.Errorf("token not found or already revoked")
	}
	return nil
}

func (r *ExternalLogRepository) InsertLog(ctx context.Context, log *externallog.ExternalLog) (*externallog.ExternalLog, error) {
	var result externallog.ExternalLog
	var tagsArray interface{} = pq.Array(log.Tags)
	if len(log.Tags) == 0 {
		tagsArray = pq.Array([]string{})
	}

	metaJSON := "{}"
	if len(log.Metadata) > 0 {
		metaJSON = string(log.Metadata)
	}

	query := `
		INSERT INTO external_logs (user_id, node_id, token_id, title, level, message, tags, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
		RETURNING id, user_id, node_id, token_id, title, level, message, tags, metadata, created_at`

	row := r.db.QueryRowxContext(ctx, query,
		log.UserID, log.NodeID, log.TokenID, log.Title, log.Level, log.Message, tagsArray, metaJSON, log.CreatedAt,
	)

	var tags pq.StringArray
	var message sql.NullString
	var nodeID sql.NullString
	var tokenID sql.NullString

	err := row.Scan(
		&result.ID,
		&result.UserID,
		&nodeID,
		&tokenID,
		&result.Title,
		&result.Level,
		&message,
		&tags,
		&result.Metadata,
		&result.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert external log: %w", err)
	}

	if nodeID.Valid {
		val := nodeID.String
		result.NodeID = &val
	}
	if tokenID.Valid {
		val := tokenID.String
		result.TokenID = &val
	}
	if message.Valid {
		val := message.String
		result.Message = &val
	}
	result.Tags = []string(tags)
	if result.Tags == nil {
		result.Tags = []string{}
	}

	return &result, nil
}

func (r *ExternalLogRepository) ListLogsFiltered(ctx context.Context, userID string, filter externallog.LogFilter, limit, offset int) ([]*externallog.ExternalLog, int, error) {
	conds := []string{"user_id = $1"}
	args := []interface{}{userID}
	n := 2

	if filter.NodeID != nil && *filter.NodeID != "" {
		if *filter.NodeID == "unlinked" {
			conds = append(conds, "node_id IS NULL")
		} else {
			conds = append(conds, fmt.Sprintf("node_id = $%d", n))
			args = append(args, *filter.NodeID)
			n++
		}
	}

	if filter.Level != nil && *filter.Level != "" && *filter.Level != "all" {
		conds = append(conds, fmt.Sprintf("level = $%d", n))
		args = append(args, strings.ToLower(*filter.Level))
		n++
	}

	if filter.Tag != nil && *filter.Tag != "" {
		conds = append(conds, fmt.Sprintf("$%d = ANY(tags)", n))
		args = append(args, *filter.Tag)
		n++
	}

	if filter.Search != nil && *filter.Search != "" {
		s := "%" + strings.ToLower(*filter.Search) + "%"
		conds = append(conds, fmt.Sprintf("(LOWER(title) LIKE $%d OR LOWER(COALESCE(message, '')) LIKE $%d)", n, n))
		args = append(args, s)
		n++
	}

	if filter.From != nil {
		conds = append(conds, fmt.Sprintf("created_at >= $%d", n))
		args = append(args, *filter.From)
		n++
	}

	if filter.To != nil {
		conds = append(conds, fmt.Sprintf("created_at <= $%d", n))
		args = append(args, *filter.To)
		n++
	}

	where := "WHERE " + strings.Join(conds, " AND ")

	var total int
	countQuery := "SELECT COUNT(*) FROM external_logs " + where
	if err := r.db.QueryRowxContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataArgs := make([]interface{}, len(args)+2)
	copy(dataArgs, args)
	dataArgs[len(args)] = limit
	dataArgs[len(args)+1] = offset

	selectQuery := fmt.Sprintf(`
		SELECT id, user_id, node_id, token_id, title, level, message, tags, metadata, created_at
		FROM external_logs %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, n, n+1)

	rows, err := r.db.QueryxContext(ctx, selectQuery, dataArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var logs []*externallog.ExternalLog
	for rows.Next() {
		var l externallog.ExternalLog
		var tags pq.StringArray
		var message sql.NullString
		var nodeID sql.NullString
		var tokenID sql.NullString

		if err := rows.Scan(
			&l.ID,
			&l.UserID,
			&nodeID,
			&tokenID,
			&l.Title,
			&l.Level,
			&message,
			&tags,
			&l.Metadata,
			&l.CreatedAt,
		); err != nil {
			return nil, 0, err
		}

		if nodeID.Valid {
			val := nodeID.String
			l.NodeID = &val
		}
		if tokenID.Valid {
			val := tokenID.String
			l.TokenID = &val
		}
		if message.Valid {
			val := message.String
			l.Message = &val
		}
		l.Tags = []string(tags)
		if l.Tags == nil {
			l.Tags = []string{}
		}

		logs = append(logs, &l)
	}

	if logs == nil {
		logs = []*externallog.ExternalLog{}
	}

	return logs, total, nil
}

func (r *ExternalLogRepository) DeleteLog(ctx context.Context, userID, logID string) error {
	query := `DELETE FROM external_logs WHERE id = $1 AND user_id = $2`
	res, err := r.db.ExecContext(ctx, query, logID, userID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err == nil && rows == 0 {
		return fmt.Errorf("log entry not found")
	}
	return nil
}

func (r *ExternalLogRepository) ClearLogs(ctx context.Context, userID string) error {
	query := `DELETE FROM external_logs WHERE user_id = $1`
	_, err := r.db.ExecContext(ctx, query, userID)
	return err
}
