package render

func matchStateText(matched bool) string {
	if matched {
		return "HIT"
	}
	return "MISS"
}
