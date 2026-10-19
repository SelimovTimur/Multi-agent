package iam

import (
	"context"
	"errors"
	"strings"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/jackc/pgx/v5"
)

// EnsureTeacher заводит активного учителя в своей организации, если такого
// ещё нет, и отдаёт его актора. Для служебных команд (cmd/seed): учётка
// появляется сразу рабочей, без письма и одобрения заявки.
//
// Повторный вызов с той же почтой ничего не меняет — ни имени, ни пароля:
// демо-данные заводятся при каждом старте, и затирать правки учителя они не
// должны. created — учётку создал этот вызов.
func (s *Service) EnsureTeacher(ctx context.Context, email, password, name, orgTitle string) (Actor, bool, error) {
	if _, err := s.store.UserByEmail(ctx, s.pool, email); err == nil {
		actor, err := s.ActorByEmail(ctx, email)
		return actor, false, err
	} else if !errors.Is(err, ErrNotFound) {
		return Actor{}, false, httpx.Internal("Не удалось прочитать пользователя").Wrap(err)
	}
	if err := validateRegistration(email, password, name); err != nil {
		return Actor{}, false, err
	}
	hash, err := HashPassword(password, s.argon())
	if err != nil {
		return Actor{}, false, httpx.Internal("Не удалось создать аккаунт").Wrap(err)
	}
	org := Organization{ID: idgen.New(), Kind: OrgKindSchool, Title: strings.TrimSpace(orgTitle), Status: "active"}
	user := User{
		ID:           idgen.New(),
		OrgID:        org.ID,
		Email:        strings.TrimSpace(email),
		DisplayName:  strings.TrimSpace(name),
		Role:         RoleTeacher,
		Status:       StatusActive,
		Origin:       OriginAdmin,
		PasswordHash: hash,
	}
	err = pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.store.CreateOrg(ctx, tx, org); err != nil {
			return err
		}
		if err := s.store.CreateUser(ctx, tx, user); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam.users SET email_verified_at = now() WHERE id = $1`, user.ID)
		return err
	})
	if err != nil {
		return Actor{}, false, httpx.Internal("Не удалось создать аккаунт").Wrap(err)
	}
	return Actor{Kind: "user", UserID: user.ID, OrgID: org.ID, Role: user.Role}, true, nil
}
