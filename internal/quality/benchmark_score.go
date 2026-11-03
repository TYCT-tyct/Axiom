package quality

import (
	"math"
	"time"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// benchmarkAnchor is the official run in the form scoring needs: the fraction each answered question passed, judged
// once when the run was recorded. The replies themselves are not kept: the judge is fixed, and the answer text is
// untrusted input no reader needs. The fractions are exactly what pairedScores compares, and they keep a stored baseline
// small enough to read on every refresh.
type benchmarkAnchor struct {
	version  string
	hash     string
	format   protocol.Format
	endpoint suppliers.EndpointID
	at       time.Time
	scores   map[string]float64 // question id -> fraction of its checks the official answer passed
}

// anchorOf turns an official run into the anchor the scorer compares against. It rejects a run of another set, without
// a time, in a format that cannot answer the questions, with a duplicate or unknown question, or without the coverage a
// comparable anchor needs. The replies are judged here once, so a stored anchor does not depend on the judge being run
// again.
func anchorOf(set *BenchmarkSet, run BenchmarkRun) (benchmarkAnchor, bool) {
	if set == nil || run.Version != set.Version || run.At.IsZero() || !benchmarkFormat(run.Format) {
		return benchmarkAnchor{}, false
	}
	answers, ok := answersByQuestion(set, run.Answers)
	if !ok {
		return benchmarkAnchor{}, false
	}
	anchor := benchmarkAnchor{
		version: set.Version, hash: set.Hash, format: run.Format, at: run.At,
		scores: make(map[string]float64, len(answers)),
	}
	for _, q := range set.Questions {
		if a, ok := answers[q.ID]; ok && a.Complete {
			anchor.scores[q.ID] = judgeBenchmarkAnswer(q, a.Reply)
		}
	}
	return anchor, anchor.valid(set)
}

// valid reports whether a loaded anchor can score anything: its identity is this set's, it answers enough questions,
// every recorded question is in the set, every fraction is a real score, and the official run scored something. A row
// that fails any of these is ignored rather than scoring every endpoint against a broken anchor.
func (a benchmarkAnchor) valid(set *BenchmarkSet) bool {
	if !a.matches(set) {
		return false
	}
	answered, points, ok := a.scored(set)
	if !ok {
		return false
	}
	return answered >= benchmarkMinPairs &&
		answered >= int(math.Ceil(benchmarkCoverage*float64(len(set.Questions)))) && points > 0
}

// matches reports whether the anchor names this set and could score a run of it.
func (a benchmarkAnchor) matches(set *BenchmarkSet) bool {
	return set != nil && a.version == set.Version && a.hash == set.Hash && !a.at.IsZero() && benchmarkFormat(a.format)
}

// scored sums the anchor's fractions and rejects a fraction outside 0..1 or a question id this set does not have: an
// unknown id is another set's answer, not a question this anchor can score.
func (a benchmarkAnchor) scored(set *BenchmarkSet) (answered int, points float64, ok bool) {
	for _, q := range set.Questions {
		score, found := a.scores[q.ID]
		if !found {
			continue
		}
		if score < 0 || score > 1 {
			return 0, 0, false
		}
		answered++
		points += score
	}
	return answered, points, answered == len(a.scores)
}

// scoreBenchmark scores a candidate run against an official run of the same set; it is the recording path's view, and
// anchorOf is what keeps the two runs comparable.
func scoreBenchmark(set *BenchmarkSet, official, candidate BenchmarkRun) (BenchmarkResult, bool) {
	anchor, ok := anchorOf(set, official)
	if !ok {
		return BenchmarkResult{}, false
	}
	return scoreAgainst(set, anchor, candidate)
}

// scoreAgainst scores a candidate run against the official anchor. The official run is the anchor: its points on the
// questions it answered are 100, and a candidate that passes more is capped at 100 because a difference of a few
// questions is noise, not a claim that it is better than official. A candidate that did not answer a question the
// official answered scores zero there; a run where the official answered fewer than benchmarkCoverage of the questions,
// or where fewer than benchmarkMinPairs pairs or benchmarkCoverage of the official answers remain, has no score (fail
// closed). The two runs must use the same call format.
func scoreAgainst(set *BenchmarkSet, anchor benchmarkAnchor, candidate BenchmarkRun) (BenchmarkResult, bool) {
	if set == nil || !anchor.valid(set) || candidate.Version != set.Version || anchor.format != candidate.Format {
		return BenchmarkResult{}, false
	}
	candidateAnswers, ok := answersByQuestion(set, candidate.Answers)
	if !ok {
		return BenchmarkResult{}, false
	}
	at := candidate.At
	if at.IsZero() {
		at = anchor.at
	}
	if at.IsZero() {
		return BenchmarkResult{}, false
	}
	pairs := pairedScores(set, anchor.scores, candidateAnswers)
	if !scoreCoverage(len(set.Questions), pairs.officialAnswered, pairs.paired) || pairs.officialPoints == 0 {
		return BenchmarkResult{}, false
	}
	confidence, ok := benchmarkConfidence(pairs.officialScores, pairs.candidateScores)
	if !ok {
		return BenchmarkResult{}, false
	}
	return BenchmarkResult{
		Version: set.Version, Hash: set.Hash, Format: candidate.Format, At: at, BaselineAt: anchor.at,
		Score:  min(100, 100*pairs.candidatePoints/pairs.officialPoints),
		Points: pairs.candidatePoints, Possible: pairs.officialPoints,
		Answered: pairs.paired, Questions: len(set.Questions),
		// Coverage is part of confidence: a run that answered fewer questions has less evidence, even when the
		// questions it did answer agree with the official run.
		Confidence: confidence * float64(pairs.paired) / float64(pairs.officialAnswered),
	}, true
}

// benchmarkPairs is the evidence of one comparison. paired counts the official questions the candidate actually
// answered, which is the coverage floor; the score and the interval use every official answer, with a missing
// candidate answer counted as zero.
type benchmarkPairs struct {
	paired, officialAnswered        int
	officialPoints, candidatePoints float64
	officialScores, candidateScores []float64
}

// pairedScores sums the question-equivalents and collects one official/candidate fraction per official answer. A
// candidate that missed a question scores zero there rather than dropping the question, so a missing first answer
// cannot make the official mean zero and reject an otherwise valid zero score.
func pairedScores(set *BenchmarkSet, official map[string]float64, candidate map[string]BenchmarkAnswer) benchmarkPairs {
	pairs := benchmarkPairs{
		officialScores:  make([]float64, 0, len(set.Questions)),
		candidateScores: make([]float64, 0, len(set.Questions)),
	}
	for _, q := range set.Questions {
		officialScore, ok := official[q.ID]
		if !ok {
			continue
		}
		pairs.officialAnswered++
		pairs.officialPoints += officialScore
		candidateScore := 0.0
		c, ok := candidate[q.ID]
		if ok && c.Complete {
			pairs.paired++
			candidateScore = judgeBenchmarkAnswer(q, c.Reply)
		}
		pairs.candidatePoints += candidateScore
		pairs.officialScores = append(pairs.officialScores, officialScore)
		pairs.candidateScores = append(pairs.candidateScores, candidateScore)
	}
	return pairs
}

// scoreCoverage is the floor for a comparable score: enough official answers, enough candidate answers, and enough of
// the official answers paired with the candidate.
func scoreCoverage(total, officialAnswered, paired int) bool {
	if officialAnswered < int(math.Ceil(benchmarkCoverage*float64(total))) {
		return false
	}
	return paired >= benchmarkMinPairs &&
		paired >= int(math.Ceil(benchmarkCoverage*float64(officialAnswered)))
}

// benchmarkConfidence turns the fractions into the score's statistical confidence. The differences carry the sampling
// noise: the standard error of the ratio of the two runs' means, by the delta method. It covers the candidate's wrong
// and missing answers and the official run's own misses.
func benchmarkConfidence(officialScores, candidateScores []float64) (float64, bool) {
	paired := len(officialScores)
	meanOfficial := sum(officialScores) / float64(paired)
	if meanOfficial <= 0 {
		return 0, false
	}
	meanCandidate := sum(candidateScores) / float64(paired)
	ratio := meanCandidate / meanOfficial
	variance := (varianceOf(candidateScores, meanCandidate) +
		ratio*ratio*varianceOf(officialScores, meanOfficial) -
		2*ratio*covarianceOf(officialScores, candidateScores, meanOfficial, meanCandidate)) /
		(float64(paired) * meanOfficial * meanOfficial)
	variance = max(0, variance) // a floating-point residue, not a negative variance
	halfWidth := benchmarkCI * 100 * math.Sqrt(variance)
	return max(0, min(1, 1-halfWidth/100)), true
}

// answersByQuestion indexes a run and rejects a question outside this set or answered twice: an unknown id makes the
// run belong to another set, and two answers for one question make it ambiguous rather than choosing one.
func answersByQuestion(set *BenchmarkSet, answers []BenchmarkAnswer) (map[string]BenchmarkAnswer, bool) {
	known := make(map[string]struct{}, len(set.Questions))
	for _, q := range set.Questions {
		known[q.ID] = struct{}{}
	}
	out := make(map[string]BenchmarkAnswer, len(answers))
	for _, a := range answers {
		if _, ok := known[a.Question]; !ok {
			return nil, false
		}
		if _, ok := out[a.Question]; ok {
			return nil, false
		}
		out[a.Question] = a
	}
	return out, true
}

func sum(values []float64) float64 {
	var total float64
	for _, v := range values {
		total += v
	}
	return total
}

func varianceOf(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var total float64
	for _, v := range values {
		total += (v - mean) * (v - mean)
	}
	return total / float64(len(values)-1)
}

func covarianceOf(a, b []float64, meanA, meanB float64) float64 {
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	var total float64
	for i := range a {
		total += (a[i] - meanA) * (b[i] - meanB)
	}
	return total / float64(len(a)-1)
}
