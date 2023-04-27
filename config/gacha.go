package config

// GachaInfo is the info for every kind of stars
type GachaInfo struct {
	Counter     int
	Probability float64
	FailBackNum int
}

// GachaTenant is the info for every different chat session
type GachaTenant struct {
	FiveStar GachaInfo
	FourStar GachaInfo
	ID       string
}
