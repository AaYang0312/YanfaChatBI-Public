package dbmanager

import (
	"context"
	"yanfachatbi/pkg/schemaobjects"
)

// GetQueryObjects returns the current reader's candidates. The caller owns the
// selection and instance reconciliation. No cached schema or data sampling is
// used here, and the source permit is retained through all catalog rows.
func (d *PostgresDriver) GetQueryObjects(ctx context.Context, db DBExecutor) ([]schemaobjects.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, release, err := openPostgresSession(ctx, db)
	if err != nil {
		return nil, err
	}
	defer release()
	objects, err := readQueryObjectsCatalog(ctx, session)
	if err != nil {
		return nil, err
	}
	// Optional definition errors must never hide a revoked source permit.
	if err = session.check(ctx); err != nil {
		return nil, err
	}
	if err = session.check(session.ctx); err != nil {
		return nil, err
	}
	return objects, nil
}
