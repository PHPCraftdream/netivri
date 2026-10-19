package subscription

import (
	"strings"
	"unicode"
)

// ambiguousISO are 2-letter codes that are also common latin words or
// letter pairs. They are only trusted when the whole name is the token,
// when bracketed or when adjacent to punctuation/digits ("[US]", "US-1").
var ambiguousISO = map[string]struct{}{
	"AN": {}, "AS": {}, "AT": {}, "BE": {}, "BY": {}, "CA": {}, "DO": {}, "ES": {}, "HE": {}, "ID": {},
	"IF": {}, "IN": {}, "IS": {}, "IT": {}, "LA": {}, "ME": {}, "MY": {}, "NO": {}, "OK": {}, "OR": {},
	"PA": {}, "SO": {}, "UP": {}, "US": {}, "WE": {},
}

// delimChars are the punctuation characters around an ISO token that make
// it explicit (brackets, pipes, dashes, digits-adjacent separators).
const delimChars = "|[](){}/\\-_.:,;#+"

// CountryFromName infers an ISO 3166-1 alpha-2 exit country from a node
// name, without any network access. It returns "" when nothing matches.
// Order: flag emoji, explicit ISO token, English names/cities, Russian
// names, Chinese names. It is best effort and never errors.
func CountryFromName(name string) string {
	if name == "" {
		return ""
	}
	if cc := countryFromFlag(name); cc != "" {
		return cc
	}
	if cc := countryFromISOToken(name); cc != "" {
		return cc
	}
	if cc := countryFromWords(name); cc != "" {
		return cc
	}
	return countryFromCJK(name)
}

// countryFromFlag reads pairs of regional indicator symbols, plus the
// black-flag tag sequences for England, Scotland and Wales.
func countryFromFlag(name string) string {
	rs := []rune(name)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			if i+1 < len(rs) && rs[i+1] >= 0x1F1E6 && rs[i+1] <= 0x1F1FF {
				cc := string(rune('A'+r-0x1F1E6)) + string(rune('A'+rs[i+1]-0x1F1E6))
				if _, ok := isoAlpha2[cc]; ok {
					return cc
				}
			}
			continue
		}
		if r == 0x1F3F4 { // black flag followed by a tag sequence
			letters := make([]rune, 0, 7)
			for j := i + 1; j < len(rs) && rs[j] >= 0xE0061 && rs[j] <= 0xE007A; j++ {
				letters = append(letters, rune('a'+rs[j]-0xE0061))
			}
			switch string(letters) {
			case "gbeng", "gbsct", "gbwls":
				return "GB"
			}
		}
	}
	return ""
}

// countryFromISOToken finds an ISO alpha-2 token delimited by non-letters
// at the start or inside brackets/pipes. Ambiguous tokens are only trusted
// when bracketed, punctuation-delimited or the whole name.
func countryFromISOToken(name string) string {
	upper := strings.ToUpper(name)
	for i := 0; i+2 <= len(upper); i++ {
		tok := upper[i : i+2]
		if !isASCIILetters(tok) {
			continue
		}
		prevBoundary := i == 0 || !isASCIILetter(upper[i-1])
		nextBoundary := i+2 == len(upper) || !isASCIILetter(upper[i+2])
		if !prevBoundary || !nextBoundary {
			continue
		}
		if _, ok := isoAlpha2[tok]; !ok {
			continue
		}
		whole := i == 0 && i+2 == len(upper)
		prevDelim := i > 0 && strings.IndexByte(delimChars, upper[i-1]) >= 0
		nextDelim := i+2 < len(upper) && strings.IndexByte(delimChars, upper[i+2]) >= 0
		if _, amb := ambiguousISO[tok]; amb && !whole && !prevDelim && !nextDelim {
			continue
		}
		return tok
	}
	return ""
}

func isASCIILetters(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isASCIILetter(s[i]) {
			return false
		}
	}
	return true
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// countryFromWords matches English and Russian country names, aliases and
// major exit cities as whole words (unigrams and bigrams).
func countryFromWords(name string) string {
	toks := wordTokens(name)
	if len(toks) == 0 {
		return ""
	}
	for i := 0; i < len(toks); i++ {
		if i+1 < len(toks) {
			if cc, ok := enBigrams[toks[i]+" "+toks[i+1]]; ok {
				return cc
			}
			if cc, ok := ruBigrams[toks[i]+" "+toks[i+1]]; ok {
				return cc
			}
		}
		if cc, ok := enWords[toks[i]]; ok {
			return cc
		}
		if cc, ok := ruWords[toks[i]]; ok {
			return cc
		}
	}
	return ""
}

func wordTokens(name string) []string {
	parts := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) })
	return parts
}

// countryFromCJK matches common Chinese country and city names as
// substrings (CJK has no word boundaries).
func countryFromCJK(name string) string {
	for _, e := range cjkTerms {
		if strings.Contains(name, e.term) {
			return e.cc
		}
	}
	return ""
}

type cjkTerm struct {
	term string
	cc   string
}

var cjkTerms = []cjkTerm{
	{"美国", "US"}, {"美國", "US"}, {"日本", "JP"}, {"香港", "HK"}, {"台湾", "TW"}, {"台灣", "TW"},
	{"新加坡", "SG"}, {"韩国", "KR"}, {"韓國", "KR"}, {"德国", "DE"}, {"德國", "DE"}, {"法国", "FR"},
	{"法國", "FR"}, {"英国", "GB"}, {"英國", "GB"}, {"荷兰", "NL"}, {"荷蘭", "NL"}, {"俄罗斯", "RU"},
	{"俄羅斯", "RU"}, {"土耳其", "TR"}, {"芬兰", "FI"}, {"印度", "IN"}, {"加拿大", "CA"}, {"澳大利亚", "AU"},
	{"澳洲", "AU"}, {"巴西", "BR"}, {"越南", "VN"}, {"泰国", "TH"}, {"泰國", "TH"}, {"马来西亚", "MY"},
	{"馬來西亞", "MY"}, {"菲律宾", "PH"}, {"菲律賓", "PH"}, {"印尼", "ID"}, {"意大利", "IT"}, {"義大利", "IT"},
	{"西班牙", "ES"}, {"波兰", "PL"}, {"波蘭", "PL"}, {"乌克兰", "UA"}, {"烏克蘭", "UA"}, {"瑞典", "SE"},
	{"瑞士", "CH"}, {"阿根廷", "AR"}, {"澳门", "MO"}, {"澳門", "MO"}, {"中国", "CN"}, {"中國", "CN"},
	{"上海", "CN"}, {"北京", "CN"}, {"广州", "CN"}, {"廣州", "CN"}, {"深圳", "CN"}, {"圣何塞", "US"},
	{"聖何塞", "US"}, {"洛杉矶", "US"}, {"洛杉磯", "US"}, {"伦敦", "GB"}, {"倫敦", "GB"}, {"东京", "JP"},
	{"東京", "JP"}, {"首尔", "KR"}, {"首爾", "KR"}, {"台北", "TW"}, {"法兰克福", "DE"}, {"法蘭克福", "DE"},
	{"阿姆斯特丹", "NL"}, {"莫斯科", "RU"}, {"伊斯坦布尔", "TR"}, {"伊斯坦布爾", "TR"}, {"赫尔辛基", "FI"},
	{"维也纳", "AT"}, {"維也納", "AT"}, {"罗马", "IT"}, {"羅馬", "IT"}, {"马德里", "ES"}, {"馬德里", "ES"},
	{"米兰", "IT"}, {"米蘭", "IT"}, {"苏黎世", "CH"}, {"蘇黎世", "CH"}, {"都柏林", "IE"},
}

var enWords = map[string]string{
	"usa": "US", "america": "US", "uk": "GB", "britain": "GB", "england": "GB", "scotland": "GB",
	"wales": "GB", "germany": "DE", "france": "FR", "netherlands": "NL", "holland": "NL",
	"japan": "JP", "singapore": "SG", "taiwan": "TW", "korea": "KR", "russia": "RU", "turkey": "TR",
	"turkiye": "TR", "finland": "FI", "sweden": "SE", "norway": "NO", "switzerland": "CH",
	"austria": "AT", "spain": "ES", "italy": "IT", "poland": "PL", "ukraine": "UA", "canada": "CA",
	"australia": "AU", "india": "IN", "brazil": "BR", "vietnam": "VN", "thailand": "TH",
	"indonesia": "ID", "malaysia": "MY", "philippines": "PH", "mexico": "MX", "argentina": "AR",
	"ireland": "IE", "belgium": "BE", "portugal": "PT", "czech": "CZ", "denmark": "DK",
	"romania": "RO", "bulgaria": "BG", "greece": "GR", "hungary": "HU", "serbia": "RS",
	"croatia": "HR", "israel": "IL", "uae": "AE", "china": "CN", "frankfurt": "DE", "berlin": "DE",
	"munich": "DE", "hamburg": "DE", "amsterdam": "NL", "london": "GB", "paris": "FR",
	"marseille": "FR", "tokyo": "JP", "osaka": "JP", "seoul": "KR", "taipei": "TW", "moscow": "RU",
	"istanbul": "TR", "helsinki": "FI", "stockholm": "SE", "warsaw": "PL", "vienna": "AT",
	"rome": "IT", "milan": "IT", "madrid": "ES", "dublin": "IE", "dubai": "AE", "zurich": "CH",
	"sydney": "AU", "melbourne": "AU", "toronto": "CA", "montreal": "CA", "vancouver": "CA",
	"jakarta": "ID", "bangkok": "TH", "hanoi": "VN", "mumbai": "IN", "delhi": "IN", "boston": "US",
	"chicago": "US", "dallas": "US", "miami": "US", "seattle": "US", "phoenix": "US",
	"atlanta": "US",
}

var enBigrams = map[string]string{
	"united states": "US", "united kingdom": "GB", "south korea": "KR", "czech republic": "CZ",
	"united arab": "AE", "arab emirates": "AE", "saudi arabia": "SA", "new york": "US",
	"los angeles": "US", "san francisco": "US", "san jose": "US", "sao paulo": "BR",
	"buenos aires": "AR", "kuala lumpur": "MY", "ho chi": "VN", "chi minh": "VN",
	"st petersburg": "RU",
}

var ruWords = map[string]string{
	"россия": "RU", "россии": "RU", "германия": "DE", "германии": "DE", "сша": "US",
	"америка": "US", "нидерланды": "NL", "нидерландов": "NL", "япония": "JP", "японии": "JP",
	"финляндия": "FI", "финляндии": "FI", "турция": "TR", "турции": "TR", "франция": "FR",
	"франции": "FR", "англия": "GB", "британия": "GB", "сингапур": "SG", "гонконг": "HK",
	"тайвань": "TW", "тайваня": "TW", "корея": "KR", "кореи": "KR", "украина": "UA",
	"украины": "UA", "польша": "PL", "польши": "PL", "швеция": "SE", "швейцария": "CH",
	"австрия": "AT", "испания": "ES", "италия": "IT", "чехия": "CZ", "дания": "DK",
	"норвегия": "NO", "бельгия": "BE", "китай": "CN", "китая": "CN", "индия": "IN",
	"бразилия": "BR", "вьетнам": "VN", "таиланд": "TH", "малайзия": "MY", "москва": "RU",
	"питер": "RU", "стокгольм": "SE", "париж": "FR", "лондон": "GB", "франкфурт": "DE",
	"амстердам": "NL", "берлин": "DE",
}

var ruBigrams = map[string]string{
	"санкт петербург": "RU", "санкт питер": "RU",
}
