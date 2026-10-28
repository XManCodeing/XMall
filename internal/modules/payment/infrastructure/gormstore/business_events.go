package gormstore

// EnableBusinessEvents is a construction-time opt-in after migration.
func (s *Store) EnableBusinessEvents() *Store { s.businessEventsEnabled = true; return s }
