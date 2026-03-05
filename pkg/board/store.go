package board

// Store abstracts persistence for the board application.
type Store interface {
	// Cards
	ListCards() ([]*Card, error)
	GetCard(id string) (*Card, error)
	InsertCard(c *Card) error
	UpdateCard(c *Card) error
	DeleteCard(id string) error
	ListCardsByColumn(column string) ([]*Card, error)
	ReinsertCard(c *Card) error

	// Projects
	ListProjects() ([]*Project, error)
	GetProject(id string) (*Project, error)
	InsertProject(p *Project) error
	DeleteProject(id string) error

	// Columns
	ListColumns() ([]Column, error)
	SeedColumns(cols []Column) error
	UpdateColumnPrompt(id, prompt string) error
}
