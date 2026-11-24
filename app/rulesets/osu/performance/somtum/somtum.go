// Package somtum is the "somtum" pp version: star rating and pp from somtum-pp, the pp the
// osu!somtum server pays. Vanilla plays get ppy's current pp; Relax gets akatsuki-pp-rs's
// osu_2019 and Autopilot akatsuki-pp-rs's calculator, as on the server.
package somtum

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/somtum-fun/somtum-pp/go/somtumpp"
	"github.com/wieku/danser-go/app/beatmap"
	"github.com/wieku/danser-go/app/beatmap/difficulty"
	"github.com/wieku/danser-go/app/rulesets/osu/performance/api"
	"github.com/wieku/danser-go/app/rulesets/osu/performance/pp260706"
	"github.com/wieku/danser-go/app/settings"
)

// Version is reported to danser as a pp version number. It is below 20260101 on purpose: somtum-pp
// gives no separate reading pp, so the pp display shows aim/speed/acc(/flashlight) only.
const Version = 20251231

// starSamples is how many points of a map get exact live star ratings in CalculateStep (each is a
// full difficulty pass); objects in between show the last sampled value. pp is always exact.
const starSamples = 64

// legacyMods keeps the osu!stable mod bits (danser adds its own above ScoreV2).
func legacyMods(diff *difficulty.Difficulty) uint32 {
	m := uint32(diff.Mods & (1<<30 - 1))
	if diff.Mods&difficulty.Daycore != 0 {
		m |= uint32(difficulty.HalfTime)
	}
	return m
}

// clockRate is a lazer rate mod's own speed; stable rate mods run at their usual speed.
func clockRate(diff *difficulty.Difficulty) float64 {
	if diff.CheckModActive(difficulty.Lazer) {
		return diff.GetSpeed()
	}
	return -1
}

type mapCache struct {
	mu   sync.Mutex
	maps map[string]*somtumpp.Map
}

var cache = &mapCache{maps: map[string]*somtumpp.Map{}}

func (c *mapCache) get(bMap *beatmap.BeatMap) *somtumpp.Map {
	path := filepath.Join(settings.General.GetSongsDir(), bMap.Dir, bMap.File)
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.maps[path]; ok {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		log.Println("somtum-pp: can't read beatmap:", err)
		c.maps[path] = nil
		return nil
	}
	m, err := somtumpp.ParseMap(b)
	if err != nil {
		log.Println("somtum-pp:", err)
	}
	if len(c.maps) > 64 { // song select walks many maps; keep memory bounded
		for k, v := range c.maps {
			if v != nil {
				v.Free()
			}
			delete(c.maps, k)
		}
	}
	c.maps[path] = m
	return m
}

func baseScore(diff *difficulty.Difficulty) somtumpp.Score {
	s := somtumpp.NewScore(0, legacyMods(diff))
	s.Lazer = diff.CheckModActive(difficulty.Lazer)
	s.ClockRate = clockRate(diff)
	return s
}

// attributes is one point of a map: what danser's displays read, plus where somtum-pp finds the map.
func attributes(m *somtumpp.Map, diff *difficulty.Difficulty, passed int) api.Attributes {
	s := baseScore(diff)
	s.Accuracy = 100
	n := m.NObjects()
	if passed < n {
		s.PassedObjects = passed
	}
	p, err := m.Calculate(s)
	if err != nil {
		return api.Attributes{ObjectCount: passed, Source: m}
	}
	return api.Attributes{Total: p.Stars, ObjectCount: passed, MaxCombo: p.MaxCombo, Source: m}
}

type DifficultyCalculator struct{}

func NewDifficultyCalculator() api.IDifficultyCalculator { return &DifficultyCalculator{} }

func (*DifficultyCalculator) CalculateSingle(bMap *beatmap.BeatMap, diff *difficulty.Difficulty) api.Attributes {
	m := cache.get(bMap)
	if m == nil {
		return pp260706.NewDifficultyCalculator().CalculateSingle(bMap, diff)
	}
	return attributes(m, diff, m.NObjects())
}

func (*DifficultyCalculator) CalculateStep(bMap *beatmap.BeatMap, diff *difficulty.Difficulty) []api.Attributes {
	m := cache.get(bMap)
	if m == nil {
		return pp260706.NewDifficultyCalculator().CalculateStep(bMap, diff)
	}
	n := m.NObjects()
	out := make([]api.Attributes, n)
	step := max(1, n/starSamples)
	var last api.Attributes
	for i := 0; i < n; i++ {
		if i%step == 0 || i == n-1 {
			last = attributes(m, diff, i+1)
		}
		out[i] = last
		out[i].ObjectCount = i + 1
	}
	return out
}

// CalculateStrainPeaks draws the strain graph with the current ppy calculator; it is only a picture.
func (*DifficultyCalculator) CalculateStrainPeaks(bMap *beatmap.BeatMap, diff *difficulty.Difficulty) api.StrainPeaks {
	return pp260706.NewDifficultyCalculator().CalculateStrainPeaks(bMap, diff)
}

func (*DifficultyCalculator) GetVersion() int { return Version }

func (*DifficultyCalculator) GetVersionMessage() string {
	return "osu!somtum (" + somtumpp.Version() + ")"
}

type PPCalculator struct{}

func NewPPCalculator() api.IPerformanceCalculator { return &PPCalculator{} }

// Calculate is pp for a score at attribs' point of the map (ObjectCount objects judged).
func (*PPCalculator) Calculate(attribs api.Attributes, score api.PerfScore, diff *difficulty.Difficulty) api.PPv2Results {
	m, ok := attribs.Source.(*somtumpp.Map)
	if !ok || m == nil {
		return api.PPv2Results{}
	}
	s := baseScore(diff)
	if attribs.ObjectCount > 0 && attribs.ObjectCount < m.NObjects() {
		s.PassedObjects = attribs.ObjectCount
	}
	if score.CountGreat < 0 { // danser's "SS at this point"
		s.Accuracy = 100 * score.Accuracy
	} else {
		s.N300, s.N100, s.N50, s.NMiss = score.CountGreat, score.CountOk, score.CountMeh, score.CountMiss
		if score.MaxCombo >= 0 {
			s.MaxCombo = score.MaxCombo
		}
		if s.Lazer && score.SliderEnd >= 0 {
			s.SliderEndHits = score.SliderEnd
		}
		if !s.Lazer && score.Score > 0 {
			s.LegacyTotalScore = score.Score
		}
	}
	p, err := m.Calculate(s)
	if err != nil {
		return api.PPv2Results{}
	}
	return api.PPv2Results{Aim: p.PPAim, Speed: p.PPSpeed, Acc: p.PPAcc, Flashlight: p.PPFlashlight, Total: p.PP}
}
