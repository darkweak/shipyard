// Package store persists templates, projects and deployments in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Deployment statuses. queued/cloning/building/deploying are "active".
const (
	StatusQueued    = "queued"
	StatusCloning   = "cloning"
	StatusBuilding  = "building"
	StatusDeploying = "deploying"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

func IsFinal(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled
}

type Variable struct {
	Name        string `json:"name"`
	Default     string `json:"default"`
	Description string `json:"description"`
}

type Template struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Dockerfile   string     `json:"dockerfile"`
	Dockerignore string     `json:"dockerignore"`
	Variables    []Variable `json:"variables"`
	DefaultPort  int        `json:"defaultPort"`
	Builtin      bool       `json:"builtin"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type Project struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	RepoURL      string `json:"repoUrl"`
	Branch       string `json:"branch"`
	AuthType     string `json:"authType"` // none | token | ssh
	AuthUsername string `json:"authUsername"`
	// AuthSecret (token or SSH private key) is write-only through the API.
	AuthSecret    string `json:"-"`
	HasAuthSecret bool   `json:"hasAuthSecret"`
	// BuildType is how the image is built: BuildDockerfile, BuildTemplate or BuildNetlify.
	BuildType      string            `json:"buildType"`
	TemplateID     string            `json:"templateId"`
	TemplateVars   map[string]string `json:"templateVars"`
	DockerfilePath string            `json:"dockerfilePath"`
	BuildContext   string            `json:"buildContext"`
	ContainerPort  int               `json:"containerPort"`
	HostPort       int               `json:"hostPort"`
	Env            map[string]string `json:"env"`
	Network        string            `json:"network"`
	Labels         map[string]string `json:"labels"`
	// Replicas and Placement (constraints) apply to Swarm deployments.
	Replicas  int      `json:"replicas"`
	Placement []string `json:"placement"`
	// Healthcheck (opt-in) probes HealthcheckPath on the container port.
	Healthcheck     bool      `json:"healthcheck"`
	HealthcheckPath string    `json:"healthcheckPath"`
	WebhookSecret   string    `json:"webhookSecret"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Deployment struct {
	ID                 string     `json:"id"`
	ProjectID          string     `json:"projectId"`
	Status             string     `json:"status"`
	Trigger            string     `json:"trigger"` // manual | webhook | rollback
	CommitSHA          string     `json:"commitSha"`
	CommitMessage      string     `json:"commitMessage"`
	Image              string     `json:"image"`
	ContainerID        string     `json:"containerId"`
	Error              string     `json:"error"`
	SourceDeploymentID string     `json:"sourceDeploymentId"`
	CreatedAt          time.Time  `json:"createdAt"`
	StartedAt          *time.Time `json:"startedAt"`
	FinishedAt         *time.Time `json:"finishedAt"`
}

// Project build types.
const (
	BuildDockerfile = "dockerfile" // the repository's own Dockerfile
	BuildTemplate   = "template"   // a Dockerfile template
	BuildNetlify    = "netlify"    // generated from the repository's netlify.toml
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS templates (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  dockerfile TEXT NOT NULL,
  dockerignore TEXT NOT NULL DEFAULT '',
  variables TEXT NOT NULL DEFAULT '[]',
  default_port INTEGER NOT NULL DEFAULT 0,
  builtin INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  repo_url TEXT NOT NULL,
  branch TEXT NOT NULL,
  auth_type TEXT NOT NULL DEFAULT 'none',
  auth_username TEXT NOT NULL DEFAULT '',
  auth_secret TEXT NOT NULL DEFAULT '',
  template_id TEXT NOT NULL DEFAULT '',
  template_vars TEXT NOT NULL DEFAULT '{}',
  dockerfile_path TEXT NOT NULL DEFAULT 'Dockerfile',
  build_context TEXT NOT NULL DEFAULT '.',
  container_port INTEGER NOT NULL DEFAULT 0,
  host_port INTEGER NOT NULL DEFAULT 0,
  env TEXT NOT NULL DEFAULT '{}',
  network TEXT NOT NULL DEFAULT '',
  labels TEXT NOT NULL DEFAULT '{}',
  webhook_secret TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS deployments (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  trigger_type TEXT NOT NULL,
  commit_sha TEXT NOT NULL DEFAULT '',
  commit_message TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  container_id TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  source_deployment_id TEXT NOT NULL DEFAULT '',
  log TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER
);
CREATE INDEX IF NOT EXISTS deployments_project ON deployments(project_id, created_at DESC);
`

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; one connection keeps things simple and
	// avoids SQLITE_BUSY between the API and the deploy workers.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	for _, c := range [][2]string{
		{"build_type", "TEXT NOT NULL DEFAULT ''"},
		{"replicas", "INTEGER NOT NULL DEFAULT 1"},
		{"placement", "TEXT NOT NULL DEFAULT '[]'"},
		{"healthcheck", "INTEGER NOT NULL DEFAULT 0"},
		{"healthcheck_path", "TEXT NOT NULL DEFAULT '/'"},
	} {
		if err := s.addColumn("projects", c[0], c[1]); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return s, nil
}

// addColumn adds a column introduced after the first release, if missing.
func (s *Store) addColumn(table, column, def string) error {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = s.db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, def))
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func NewSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- meta ----

func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- templates ----

const templateCols = `id, name, description, dockerfile, dockerignore, variables, default_port, builtin, created_at, updated_at`

func scanTemplate(row interface{ Scan(...any) error }) (*Template, error) {
	var t Template
	var vars string
	var created, updated int64
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Dockerfile, &t.Dockerignore, &vars,
		&t.DefaultPort, &t.Builtin, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal([]byte(vars), &t.Variables); err != nil {
		return nil, err
	}
	if t.Variables == nil {
		t.Variables = []Variable{}
	}
	t.CreatedAt, t.UpdatedAt = fromMillis(created), fromMillis(updated)
	return &t, nil
}

func (s *Store) ListTemplates(ctx context.Context) ([]*Template, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateCols+` FROM templates ORDER BY builtin DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Template{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTemplate(ctx context.Context, id string) (*Template, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateCols+` FROM templates WHERE id = ?`, id))
}

func (s *Store) CreateTemplate(ctx context.Context, t *Template) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	now := time.Now()
	t.CreatedAt, t.UpdatedAt = now, now
	vars, _ := json.Marshal(nonNilVars(t.Variables))
	_, err := s.db.ExecContext(ctx, `INSERT INTO templates(`+templateCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.Description, t.Dockerfile, t.Dockerignore, string(vars), t.DefaultPort, t.Builtin,
		millis(now), millis(now))
	return err
}

func (s *Store) UpdateTemplate(ctx context.Context, t *Template) error {
	t.UpdatedAt = time.Now()
	vars, _ := json.Marshal(nonNilVars(t.Variables))
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET name=?, description=?, dockerfile=?, dockerignore=?,
		variables=?, default_port=?, updated_at=? WHERE id=?`,
		t.Name, t.Description, t.Dockerfile, t.Dockerignore, string(vars), t.DefaultPort, millis(t.UpdatedAt), t.ID)
	return affected(res, err)
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE template_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: template is used by %d project(s)", ErrConflict, n)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM templates WHERE id = ?`, id)
	return affected(res, err)
}

// ---- projects ----

const projectCols = `id, name, slug, repo_url, branch, auth_type, auth_username, auth_secret, build_type, template_id,
	template_vars, dockerfile_path, build_context, container_port, host_port, env, network, labels,
	replicas, placement, healthcheck, healthcheck_path, webhook_secret, created_at, updated_at`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var vars, env, labels, placement string
	var created, updated int64
	if err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.RepoURL, &p.Branch, &p.AuthType, &p.AuthUsername,
		&p.AuthSecret, &p.BuildType, &p.TemplateID, &vars, &p.DockerfilePath, &p.BuildContext, &p.ContainerPort,
		&p.HostPort, &env, &p.Network, &labels, &p.Replicas, &placement, &p.Healthcheck, &p.HealthcheckPath, &p.WebhookSecret, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := unmarshalMap(vars, &p.TemplateVars); err != nil {
		return nil, err
	}
	if err := unmarshalMap(env, &p.Env); err != nil {
		return nil, err
	}
	if err := unmarshalMap(labels, &p.Labels); err != nil {
		return nil, err
	}
	p.Placement = []string{}
	if err := json.Unmarshal([]byte(placement), &p.Placement); err != nil {
		return nil, err
	}
	if p.BuildType == "" { // rows created before build types existed
		p.BuildType = BuildDockerfile
		if p.TemplateID != "" {
			p.BuildType = BuildTemplate
		}
	}
	p.HasAuthSecret = p.AuthSecret != ""
	p.CreatedAt, p.UpdatedAt = fromMillis(created), fromMillis(updated)
	return &p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]*Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectCols+` FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, id string) (*Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
}

func (s *Store) SlugExists(ctx context.Context, slug string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE slug = ?`, slug).Scan(&n)
	return n > 0, err
}

func (s *Store) CreateProject(ctx context.Context, p *Project) error {
	if p.ID == "" {
		p.ID = NewID()
	}
	if p.WebhookSecret == "" {
		p.WebhookSecret = NewSecret()
	}
	now := time.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects(`+projectCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Slug, p.RepoURL, p.Branch, p.AuthType, p.AuthUsername, p.AuthSecret, p.BuildType, p.TemplateID,
		marshalMap(p.TemplateVars), p.DockerfilePath, p.BuildContext, p.ContainerPort, p.HostPort,
		marshalMap(p.Env), p.Network, marshalMap(p.Labels), p.Replicas, marshalList(p.Placement),
		p.Healthcheck, p.HealthcheckPath, p.WebhookSecret, millis(now), millis(now))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("%w: a project with slug %q already exists", ErrConflict, p.Slug)
	}
	p.HasAuthSecret = p.AuthSecret != ""
	return err
}

func (s *Store) UpdateProject(ctx context.Context, p *Project) error {
	p.UpdatedAt = time.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE projects SET name=?, repo_url=?, branch=?, auth_type=?,
		auth_username=?, auth_secret=?, build_type=?, template_id=?, template_vars=?, dockerfile_path=?, build_context=?,
		container_port=?, host_port=?, env=?, network=?, labels=?, replicas=?, placement=?, healthcheck=?, healthcheck_path=?, webhook_secret=?, updated_at=? WHERE id=?`,
		p.Name, p.RepoURL, p.Branch, p.AuthType, p.AuthUsername, p.AuthSecret, p.BuildType, p.TemplateID,
		marshalMap(p.TemplateVars), p.DockerfilePath, p.BuildContext, p.ContainerPort, p.HostPort,
		marshalMap(p.Env), p.Network, marshalMap(p.Labels), p.Replicas, marshalList(p.Placement),
		p.Healthcheck, p.HealthcheckPath, p.WebhookSecret, millis(p.UpdatedAt), p.ID)
	p.HasAuthSecret = p.AuthSecret != ""
	return affected(res, err)
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	return affected(res, err)
}

// ---- deployments ----

const deploymentCols = `id, project_id, status, trigger_type, commit_sha, commit_message, image, container_id,
	error, source_deployment_id, created_at, started_at, finished_at`

func scanDeployment(row interface{ Scan(...any) error }) (*Deployment, error) {
	var d Deployment
	var created int64
	var started, finished sql.NullInt64
	if err := row.Scan(&d.ID, &d.ProjectID, &d.Status, &d.Trigger, &d.CommitSHA, &d.CommitMessage, &d.Image,
		&d.ContainerID, &d.Error, &d.SourceDeploymentID, &created, &started, &finished); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	d.CreatedAt = fromMillis(created)
	if started.Valid {
		t := fromMillis(started.Int64)
		d.StartedAt = &t
	}
	if finished.Valid {
		t := fromMillis(finished.Int64)
		d.FinishedAt = &t
	}
	return &d, nil
}

func (s *Store) CreateDeployment(ctx context.Context, d *Deployment) error {
	if d.ID == "" {
		d.ID = NewID()
	}
	d.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployments(id, project_id, status, trigger_type, commit_sha,
		commit_message, image, source_deployment_id, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ProjectID, d.Status, d.Trigger, d.CommitSHA, d.CommitMessage, d.Image, d.SourceDeploymentID,
		millis(d.CreatedAt))
	return err
}

// SaveDeployment writes every mutable field of d (not the log).
func (s *Store) SaveDeployment(ctx context.Context, d *Deployment) error {
	res, err := s.db.ExecContext(ctx, `UPDATE deployments SET status=?, commit_sha=?, commit_message=?, image=?,
		container_id=?, error=?, started_at=?, finished_at=? WHERE id=?`,
		d.Status, d.CommitSHA, d.CommitMessage, d.Image, d.ContainerID, d.Error,
		nullMillis(d.StartedAt), nullMillis(d.FinishedAt), d.ID)
	return affected(res, err)
}

func (s *Store) SetDeploymentLog(ctx context.Context, id, log string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployments SET log = ? WHERE id = ?`, log, id)
	return err
}

func (s *Store) GetDeploymentLog(ctx context.Context, id string) (string, error) {
	var l string
	err := s.db.QueryRowContext(ctx, `SELECT log FROM deployments WHERE id = ?`, id).Scan(&l)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return l, err
}

func (s *Store) GetDeployment(ctx context.Context, id string) (*Deployment, error) {
	return scanDeployment(s.db.QueryRowContext(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE id = ?`, id))
}

func (s *Store) ListDeployments(ctx context.Context, projectID string, limit int) ([]*Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE project_id = ?
		ORDER BY created_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// LatestDeployments returns the most recent deployment of every project, keyed by project ID.
func (s *Store) LatestDeployments(ctx context.Context) (map[string]*Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentCols+` FROM deployments d
		WHERE created_at = (SELECT MAX(created_at) FROM deployments WHERE project_id = d.project_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out[d.ProjectID] = d
	}
	return out, rows.Err()
}

// FailInterrupted marks deployments left active by a previous process as failed.
func (s *Store) FailInterrupted(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE deployments SET status = ?, error = 'interrupted: server restarted',
		finished_at = ? WHERE status IN (?, ?, ?, ?)`, StatusFailed, millis(time.Now()),
		StatusQueued, StatusCloning, StatusBuilding, StatusDeploying)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- helpers ----

func millis(t time.Time) int64      { return t.UnixMilli() }
func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func nullMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func marshalMap(m map[string]string) string {
	if m == nil {
		return "{}"
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func marshalList(l []string) string {
	if l == nil {
		return "[]"
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func unmarshalMap(s string, dst *map[string]string) error {
	m := map[string]string{}
	if s != "" {
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return err
		}
	}
	*dst = m
	return nil
}

func nonNilVars(v []Variable) []Variable {
	if v == nil {
		return []Variable{}
	}
	return v
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
