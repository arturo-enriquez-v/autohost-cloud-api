package project

// Service handles business logic for projects.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(userID, name string) (*Project, error) {
	if name == "" {
		return nil, ErrInvalidProject
	}
	p := &Project{UserID: userID, Name: name}
	return s.repo.Create(p)
}

func (s *Service) GetByID(id string) (*Project, error) {
	return s.repo.FindByID(id)
}

func (s *Service) GetByUser(userID string) ([]*Project, error) {
	return s.repo.FindByUserID(userID)
}

func (s *Service) Update(id, userID, name string) (*Project, error) {
	p, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if p.UserID != userID {
		return nil, ErrUnauthorized
	}
	if name != "" {
		p.Name = name
	}
	return s.repo.Update(p)
}

func (s *Service) Delete(id, userID string) error {
	p, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if p.UserID != userID {
		return ErrUnauthorized
	}
	return s.repo.Delete(id)
}
