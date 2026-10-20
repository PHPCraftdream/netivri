package subscription

import "sort"

// CountryCount is the number of nodes that exit in one country.
type CountryCount struct {
	Code  string `json:"code"`  // ISO 3166-1 alpha-2, upper case
	Nodes int    `json:"nodes"` // how many nodes carry the code
}

// CountryCounts summarises nodes by exit country. The result is sorted by
// code; nodes without a country are not listed but returned as unknown.
func CountryCounts(nodes []Node) (counts []CountryCount, unknown int) {
	totals := make(map[string]int)
	for _, n := range nodes {
		if n.Country == "" {
			unknown++
			continue
		}
		totals[n.Country]++
	}
	counts = make([]CountryCount, 0, len(totals))
	for code, num := range totals {
		counts = append(counts, CountryCount{Code: code, Nodes: num})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].Code < counts[j].Code })
	return counts, unknown
}
