package service

import (
	"context"
	"database/sql"
	"errors"
)

// TelegramConfig returns the decrypted integration configuration, never exposed by API.
func (s *Service) TelegramConfig(ctx context.Context) ([]byte, error) {
	var blob []byte
	err := s.store.DB().QueryRowContext(ctx, `SELECT config FROM telegram_integration WHERE id=1`).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.sealer.Open(blob)
}
func (s *Service) SaveTelegramConfig(ctx context.Context, plain []byte) error {
	blob, err := s.sealer.Seal(plain)
	if err != nil {
		return err
	}
	_, err = s.store.DB().ExecContext(ctx, `INSERT INTO telegram_integration(id,config) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET config=excluded.config`, blob)
	return err
}
