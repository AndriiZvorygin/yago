package websearch

func (r *engineRace) exhaustedOutcome() (bool, error) {
	allRateLimited := true
	var lastErr error
	for preference, err := range r.errors {
		if r.limited[preference] {
			continue
		}
		if err == nil {
			return false, nil
		}
		allRateLimited = false
		lastErr = err
	}

	return allRateLimited, lastErr
}
