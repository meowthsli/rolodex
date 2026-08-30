package utils

import (
	"sort"
	"strings"
)

// ruEn transliterates Cyrillic lowercase letters to Latin. Mixed-case input is
// lowercased before lookup, so only lowercase entries are needed.
var ruEn = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "c", 'ч': "ch", 'ш': "sh", 'щ': "sch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

// transliterate converts a string to a lowercase ASCII form, mapping Cyrillic
// letters and keeping Latin letters/digits; every other rune is dropped.
func transliterate(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if m, ok := ruEn[r]; ok {
			b.WriteString(m)
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isWordRune reports whether r may belong to a name token (letter or digit);
// punctuation/spaces/underscores are separators. Note that 'ё'/'Ё' (U+0451/U+0401)
// lie outside the а–я range and must be allowed explicitly.
func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		(r >= 'а' && r <= 'я') || r == 'ё' ||
		(r >= 'А' && r <= 'Я') || r == 'Ё'
}

// translitFold applies safe, language-wide Russian->Latin transliteration
// variant rules so that different spellings of the same name collapse onto one
// form. Applied per token before the diminutive dictionary. Multi-character
// rules run first. Deliberately conservative: it does NOT merge distinct
// letters (e.g. ш/щ, ч/ц) to avoid false merges.
func translitFold(t string) string {
	repl := []struct{ from, to string }{
		{"tch", "ch"}, // Отчего/Ч: Овчинников = Ovtchinnikov
		{"kh", "h"},   // х: Михаил = Mikhail/Mihail
		{"zh", "j"},   // ж: Женя = Zhenya/Jenya
		{"ts", "c"},   // ц: Цап = Tsap/Tsap
		{"ya", "ia"},  // я
		{"yu", "iu"},  // ю
		{"ye", "ie"},  // е (initial): Егор = Yegor/Iegor
		{"yo", "io"},  // ё
		{"ey", "ei"},  // ей
		{"ej", "ei"},  // ей (Latin ej)
		{"iy", "i"},   // ий
		{"ii", "i"},   // ий (и + й both -> i)
		{"ij", "i"},   // ий (Latin ij)
		{"yi", "i"},   // ый
		{"x", "ks"},   // ks: Алекс = Aleks/Alex
	}
	for _, r := range repl {
		t = strings.ReplaceAll(t, r.from, r.to)
	}
	// Trailing 'y' (name/adjective/patronymic ending) collapses to 'i':
	// Дмитрий = Dmitriy/Dmitry, Василий = Vasily/Vasili.
	if strings.HasSuffix(t, "y") {
		t = t[:len(t)-1] + "i"
	}
	return t
}

// nameTokenCanonicalRaw maps a name token (in natural transliteration) onto its
// canonical full-name form. It covers diminutives / hypocoristics (Петя -> Пётр,
// Саша -> Александр) and irregular full-name transliteration variants. Both keys
// and values are written in natural form; nameTokenCanonical below folds them
// through translitFold so they match the folded incoming tokens.
var nameTokenCanonicalRaw = map[string]string{
	// Александр / Александра
	"sasha": "aleksandr", "shura": "aleksandr", "sania": "aleksandr", "sashka": "aleksandr", "sashenka": "aleksandr",
	// Алексей
	"alesha": "aleksei", "liosha": "aleksei", "lesha": "aleksei", "leshka": "aleksei", "lioshka": "aleksei", "aleks": "aleksei",
	// Андрей
	"andriusha": "andrei", "andriukha": "andrei", "dusia": "andrei",
	// Антон
	"antosha": "anton", "tosha": "anton", "tonya": "anton", "antoshka": "anton",
	// Борис
	"boria": "boris", "boba": "boris",
	// Владимир
	"vova": "vladimir", "volodia": "vladimir", "vovik": "vladimir", "volodka": "vladimir",
	// Виктор
	"vitia": "viktor", "vitenka": "viktor",
	// Василий
	"vasia": "vasilii", "vasenka": "vasilii", "vasilek": "vasilii",
	// Дмитрий
	"dima": "dmitrii", "dimka": "dmitrii", "mitia": "dmitrii", "mitka": "dmitrii",
	// Евгений
	"zhenya": "evgenii", "zhenka": "evgenii",
	// Георгий / Егор
	"egor": "georgii", "iegor": "georgii", "gosha": "georgii", "zhora": "georgii", "gera": "georgii",
	// Иван
	"vania": "ivan", "vanka": "ivan", "vanechka": "ivan", "vaniusa": "ivan",
	// Игорь
	"igorek": "igor",
	// Илья
	"iliusha": "ilya", "ilusha": "ilya",
	// Кирилл
	"kiria": "kirill", "kiriusa": "kirill", "kirusa": "kirill",
	// Константин
	"kostia": "konstantin", "kosia": "konstantin", "kostik": "konstantin",
	// Лев
	"liova": "lev", "liovushka": "lev",
	// Леонид
	"lionia": "leonid", "leonia": "leonid",
	// Максим
	"maks": "maksim", "maksimka": "maksim",
	// Михаил (michail is a transliteration variant)
	"misa": "mikhail", "misha": "mikhail", "miha": "mikhail", "mishka": "mikhail", "michail": "mikhail",
	// Николай
	"kolya": "nikolai", "nikolia": "nikolai", "kolenka": "nikolai",
	// Павел
	"pasha": "pavel", "pavlik": "pavel",
	// Пётр (incl. Latin forms)
	"petia": "petr", "petya": "petr", "piotr": "petr", "pyotr": "petr", "petenka": "petr", "petrusha": "petr",
	// Роман
	"roma": "roman", "romka": "roman",
	// Сергей
	"seriozha": "sergei", "serega": "sergei", "serezha": "sergei", "seriozhka": "sergei",
	// Степан
	"stiosha": "stepan", "stiopa": "stepan", "stesha": "stepan",
	// Фёдор
	"fedya": "fedor", "fedenka": "fedor",
	// Юрий
	"ura": "iurii", "iurka": "iurii",
	// Яков
	"iasha": "iakov", "iashka": "iakov",
	// Артём
	"artiomka": "artem", "tioma": "artem", "tema": "artem",
	// Семён
	"senechka": "semen", "sionia": "semen",
	// Тимофей
	"tima": "timofei", "timosha": "timofei",
	// Филипп
	"filia": "filipp", "filippka": "filipp",
	// Анатолий
	"tolia": "anatolii", "tolik": "anatolii",
	// Виталий
	"vitalik": "vitalii",
	// Эдуард
	"edik": "eduard",
	// Родион
	"rodia": "rodion",
	// Тарас
	"tarasik": "taras",
	// Фома
	"fomka": "foma",

	// --- Female ---
	// Анастасия
	"nastia": "anastasia", "asia": "anastasia", "nastenka": "anastasia",
	// Анна
	"ania": "anna", "aniuta": "anna", "aneta": "anna", "niura": "anna", "niusa": "anna", "anechka": "anna",
	// Варвара
	"varia": "varvara", "vava": "varvara",
	// Вера
	"verochka": "vera", "verusia": "vera",
	// Виктория
	"vika": "viktoria", "vikusia": "viktoria",
	// Екатерина
	"katia": "ekaterina", "katiusa": "ekaterina", "katerina": "ekaterina", "katrinka": "ekaterina",
	// Елена
	"lena": "elena", "lenochka": "elena", "alena": "elena", "elia": "elena", "lialia": "elena",
	// Елизавета
	"liza": "elizaveta", "lizochka": "elizaveta",
	// Ирина
	"ira": "irina", "irochka": "irina",
	// Мария
	"masha": "maria", "mania": "maria", "marisha": "maria", "marusia": "maria", "mashenka": "maria",
	// Марина
	"mariska": "marina", "mara": "marina",
	// Наталья
	"natasha": "natalia", "nata": "natalia", "natusia": "natalia",
	// Ольга
	"olia": "olga", "olechka": "olga", "olgusia": "olga",
	// Светлана
	"sveta": "svetlana", "svetik": "svetlana",
	// София
	"sonia": "sofia", "sofia": "sofia", "sonechka": "sofia",
	// Татьяна
	"tania": "tatiana", "taniusa": "tatiana", "tatusia": "tatiana",
	// Юлия
	"iulia": "iulia", "iulka": "iulia",
	// Дарья
	"dasha": "daria", "dashenka": "daria",
	// Ксения
	"ksiusha": "ksenia",
	// Полина
	"polina": "polina", "polinka": "polina",
	// Галина
	"galia": "galina", "galochka": "galina",
	// Лариса
	"lariska": "larisa", "lara": "larisa",
	// Любовь
	"liuba": "liubov", "liubochka": "liubov",
	// Людмила
	"liuda": "liudmila", "mila": "liudmila", "liusia": "liudmila",
	// Маргарита
	"margo": "margarita", "rita": "margarita",
	// Тамара
	"tomochka": "tamara",
	// Яна
	"iana": "iana", "ianochka": "iana",
}

// nameTokenCanonical is nameTokenCanonicalRaw with both keys and values run
// through translitFold, so entries can be authored in natural transliteration.
var nameTokenCanonical = func() map[string]string {
	m := make(map[string]string, len(nameTokenCanonicalRaw))
	for k, v := range nameTokenCanonicalRaw {
		m[translitFold(k)] = translitFold(v)
	}
	return m
}()

// canonicalToken normalizes a single token: first the systematic transliteration
// fold, then the diminutive/full-name dictionary.
func canonicalToken(t string) string {
	t = translitFold(t)
	if c, ok := nameTokenCanonical[t]; ok {
		return c
	}
	return t
}

// CanonKey produces a deterministic, order/case/punctuation-invariant key for a
// name: split into word tokens, transliterate each, fold transliteration
// variants and diminutives, sort the tokens and join with "_". This makes
// "Евгений Голанд" and "Голанд Евгений" collide, aligns with the model's
// uppercase ids (GORIN_EVGENIY -> evgeni_gorin), and unifies transliteration and
// diminutive variants (Алексей == Alexey, Пётр == Петя == Petya).
func CanonKey(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return !isWordRune(r) })
	words := make([]string, 0, len(fields))
	for _, f := range fields {
		if w := canonicalToken(transliterate(f)); w != "" {
			words = append(words, w)
		}
	}
	sort.Strings(words)
	return strings.Join(words, "_")
}

// canonTokens returns the sorted, de-duplicated token set of a name's CanonKey.
func canonTokens(s string) []string {
	parts := strings.Split(CanonKey(s), "_")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, p := range parts {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// Similarity scores how likely two names refer to the same entity. It is the
// Jaccard index over CanonKey token sets, with an abbreviation bonus: a token
// like "p." (single letter + dot) is treated as matching any token starting
// with that letter, so "Евгений П." matches "Евгений Петров".
func Similarity(a, b string) float64 {
	ta := canonTokens(a)
	tb := canonTokens(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 0
	}
	setB := make(map[string]bool, len(tb))
	for _, t := range tb {
		setB[t] = true
	}
	inter := 0
	for _, t := range ta {
		if setB[t] {
			inter++
			continue
		}
		if len(t) == 2 && t[1] == '.' && t[0] >= 'a' && t[0] <= 'z' {
			for _, u := range tb {
				if strings.HasPrefix(u, t[0:1]) {
					inter++
					break
				}
			}
		}
	}
	union := len(ta) + len(tb) - inter
	if union <= 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
