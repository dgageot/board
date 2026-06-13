package board

// Store abstracts persistence for the board application.
type Store interface {
	// Cards
	ListCards() ([]*Card, error)
	GetCard(id string) (*Card, error)
	InsertCard(c *Card) error
	UpdateCard(c *Card) error
	// UpdateCardStatus persists only the status field of a card. Background
	// goroutines (poller, remote watcher) use it instead of [UpdateCard] so a
	// stale snapshot of the row cannot silently revert concurrent edits made
	// by the move-card handler.
	UpdateCardStatus(id string, status CardStatus) error
	// UpdateCardSession persists only the session field of a card. Same
	// rationale as [UpdateCardStatus]: callers holding a stale snapshot must
	// not revert concurrent column or status updates.
	UpdateCardSession(id, session string) error
	DeleteCard(id string) error
	ListCardsByColumn(column string) ([]*Card, error)
	ReinsertCard(c *Card) error

	// Projects
	ListProjects() ([]*Project, error)
	GetProject(id string) (*Project, error)
	InsertProject(p *Project) error
	DeleteProject(id string) error
	ReorderProjects(ids []string) error

	// Columns
	ListColumns() ([]Column, error)
	SeedColumns(cols []Column) error
	UpdateColumnPrompt(id, prompt string) error
}
