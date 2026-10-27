package session

// Accounting describes records on the selected branch, including records before
// compaction. These are stored counters, not a tokenizer or a price estimate.
type Accounting struct {
	Usage         Usage
	Attempts      int
	ReturnedModel string
}

func (s *Store) Accounting() (Accounting, error) {
	path, err := s.MetadataPath()
	if err != nil {
		return Accounting{}, err
	}
	var result Accounting
	add := func(usage Usage) {
		result.Attempts++
		result.Usage.Input += usage.Input
		result.Usage.Output += usage.Output
		result.Usage.CacheRead += usage.CacheRead
		result.Usage.CacheWrite += usage.CacheWrite
		result.Usage.TotalTokens += usage.TotalTokens
		addOptional := func(dest **float64, value *float64) {
			if value != nil {
				if *dest == nil {
					*dest = new(float64)
				}
				**dest += *value
			}
		}
		addOptional(&result.Usage.CacheWrite1h, usage.CacheWrite1h)
		addOptional(&result.Usage.Reasoning, usage.Reasoning)
	}
	for _, e := range path {
		if e.Message != nil && e.Message.Message.Assistant != nil {
			a := e.Message.Message.Assistant
			add(a.Usage)
			result.ReturnedModel = a.Model
			if a.ResponseModel != nil {
				result.ReturnedModel = *a.ResponseModel
			}
		}
		if e.Usage != nil {
			add(e.Usage.Usage)
		}
		if e.Compaction != nil && e.Compaction.Usage != nil {
			add(*e.Compaction.Usage)
		}
	}
	return result, nil
}
