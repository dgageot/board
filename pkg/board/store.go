package board

// Store abstracts persistence for the board application.
type Store interface {
	// Cards
	ListCards() ([]*Card, error)
	GetCard(id string) (*Card, error)
	InsertCard(c *Card) error
	// UpdateCardStatus persists only the status field of a card. Background
	// goroutines (the controller's watchers) use it so a stale snapshot of the
	// row cannot silently revert concurrent edits made by the move-card
	// handler.
	UpdateCardStatus(id string, status CardStatus) error
	// UpdateCardTitle persists only the title field of a card. Used by the
	// controller when a session_title event arrives, without clobbering
	// concurrent edits.
	UpdateCardTitle(id, title string) error
	// UpdateCardCost persists only the cost field of a card. Used by the
	// controller to mirror the agent session's cumulative cost from its
	// snapshot, without clobbering concurrent edits.
	UpdateCardCost(id string, cost float64) error
	DeleteCard(id string) error
	ListCardsByColumn(column string) ([]*Card, error)
	// MoveCard atomically moves a card to the given column and re-inserts it
	// at the end of the ordering. The row is re-read inside the transaction so
	// the move preserves the current status; when requireIdle is set a running
	// card is rejected, atomically with the move.
	MoveCard(id, column string, requireIdle bool) (*Card, error)

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
