package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Products are the hub the three revenue-bearing business lines meet at: a
// contract sells one, a content piece promotes one, a release iterates one.
// The table is deliberately thin - positioning and research live in documents.

const productColumns = `id, name, kind, status, positioning, repo_url, owner,
                        current_release_id, launch_date, version, created_at, updated_at`

func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	var p Product
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.Status, &p.Positioning, &p.RepoURL,
		&p.Owner, &p.CurrentReleaseID, &p.LaunchDate, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func loadProduct(ctx context.Context, tx *sql.Tx, id string) (*Product, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+productColumns+` FROM products WHERE id = ?`, id)
	p, err := scanProduct(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("product %s does not exist", id)
	}
	return p, err
}

// CreateProductInput is the payload of `product.create`.
type CreateProductInput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Status      string `json:"status,omitempty"`
	Positioning string `json:"positioning,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	Owner       string `json:"owner,omitempty"`
	LaunchDate  string `json:"launch_date,omitempty"`
}

func (in *CreateProductInput) normalize() error {
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.Kind == "" {
		in.Kind = "product"
	}
	if !validProductKind[in.Kind] {
		return protocol.BadInput("kind must be product|service|solution")
	}
	if in.Status == "" {
		in.Status = "concept"
	}
	if !validProductStatus[in.Status] {
		return protocol.BadInput("status must be concept|developing|released|maintained|sunset")
	}
	if in.LaunchDate != "" {
		if err := ValidateDate("launch_date", in.LaunchDate); err != nil {
			return err
		}
	}
	return nil
}

// CreateProduct registers a product, service or solution.
func (s *Store) CreateProduct(ctx context.Context, wc WriteContext, in CreateProductInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "product.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		id := system.NewID("prod")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO products (id, name, kind, status, positioning, repo_url, owner,
                                  launch_date, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.Name, in.Kind, in.Status, nullString(in.Positioning),
			nullString(in.RepoURL), nullString(in.Owner), nullString(in.LaunchDate),
			ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "product", id, "created", nil, in); err != nil {
			return nil, err
		}
		p, err := loadProduct(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: p,
			Changes: []protocol.Change{{EntityType: "product", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"products"}}},
		}, nil
	})
}

// UpdateProductInput patches a product under optimistic concurrency.
type UpdateProductInput struct {
	ProductID        string  `json:"product_id"`
	ExpectedVersion  int64   `json:"expected_version"`
	Name             *string `json:"name,omitempty"`
	Kind             *string `json:"kind,omitempty"`
	Status           *string `json:"status,omitempty"`
	Positioning      *string `json:"positioning,omitempty"`
	RepoURL          *string `json:"repo_url,omitempty"`
	Owner            *string `json:"owner,omitempty"`
	CurrentReleaseID *string `json:"current_release_id,omitempty"`
	LaunchDate       *string `json:"launch_date,omitempty"`
}

// UpdateProduct applies a patch under optimistic concurrency control. Pointing
// current_release_id at a release is checked here rather than left to the
// foreign key, so a typo reads as NOT_FOUND instead of a constraint error.
func (s *Store) UpdateProduct(ctx context.Context, wc WriteContext, in UpdateProductInput) (*Result, error) {
	if in.ProductID == "" {
		return nil, protocol.BadInput("product_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the product first")
	}
	if in.Kind != nil && !validProductKind[*in.Kind] {
		return nil, protocol.BadInput("kind must be product|service|solution")
	}
	if in.Status != nil && !validProductStatus[*in.Status] {
		return nil, protocol.BadInput("status must be concept|developing|released|maintained|sunset")
	}
	if in.LaunchDate != nil && *in.LaunchDate != "" {
		if err := ValidateDate("launch_date", *in.LaunchDate); err != nil {
			return nil, err
		}
	}
	return s.execute(ctx, "product.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadProduct(ctx, tx, in.ProductID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("product", in.ExpectedVersion, before.Version)
		}
		if in.CurrentReleaseID != nil && *in.CurrentReleaseID != "" {
			if err := requireExists(ctx, tx, "releases", *in.CurrentReleaseID, "release"); err != nil {
				return nil, err
			}
		}
		set := newPatch()
		set.str("name", in.Name)
		set.str("kind", in.Kind)
		set.str("status", in.Status)
		set.str("positioning", in.Positioning)
		set.str("repo_url", in.RepoURL)
		set.str("owner", in.Owner)
		set.str("current_release_id", in.CurrentReleaseID)
		set.str("launch_date", in.LaunchDate)
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "products", "product", in.ProductID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		eventType := "updated"
		if in.Status != nil {
			eventType = "status_changed"
		}
		after, err := loadProduct(ctx, tx, in.ProductID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "product", in.ProductID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "product", EntityID: in.ProductID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"products"}}},
		}, nil
	})
}

// GetProduct reads one product.
func (s *Store) GetProduct(ctx context.Context, id string) (*Product, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+productColumns+` FROM products WHERE id = ?`, id)
	p, err := scanProduct(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("product %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return p, nil
}

// ProductFilter narrows ListProducts.
type ProductFilter struct {
	Kind   string
	Status string
	Search string
	Limit  int
}

// ListProducts lists products, newest activity first.
func (s *Store) ListProducts(ctx context.Context, f ProductFilter) ([]*Product, error) {
	query := `SELECT ` + productColumns + ` FROM products WHERE 1=1`
	var args []any
	if f.Kind != "" {
		query += " AND kind = ?"
		args = append(args, f.Kind)
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.Search != "" {
		query += " AND name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Product{}
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, p)
	}
	return out, sqlite.Classify(rows.Err())
}
