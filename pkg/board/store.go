package board

// Store abstracts persistence for the board application.
type Store interface {
	// Cards
	ListCards() ([]*Card, error)
	GetCard(id string) (*Card, error)
	InsertCard(c *Card) error
	// InsertCardInFirstColumn inserts the card into the board's first column,
	// resolved atomically inside the insert transaction so a concurrent
	// column replace cannot leave the card in a deleted column.
	InsertCardInFirstColumn(c *Card) error
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
	// UpdateCardPRURL persists only the pr_url field of a card. Used by the
	// controller when it discovers the pull request opened for a Push-column
	// card, without clobbering concurrent edits.
	UpdateCardPRURL(id, prURL string) error
	DeleteCard(id string) error
	ListCardsByColumn(column string) ([]*Card, error)
	// MoveCard atomically moves a card to the given column and re-inserts it
	// at the end of the ordering. The row is re-read inside the transaction so
	// the move preserves the current status; when requireIdle is set a running
	// card is rejected, atomically with the move. The destination column must
	// exist, checked atomically with the move.
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
	// ReplaceColumns atomically replaces the whole column set with cols, in
	// order. Deleting a column that still holds cards is rejected with
	// errColumnHasCards, atomically with the replace, so no card can be left
	// pointing at a column that no longer exists.
	ReplaceColumns(cols []Column) error
}
