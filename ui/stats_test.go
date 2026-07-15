package ui

import (
	"math"
	"math/rand/v2"
	"testing"

	"elevatorsim/domain"
	"elevatorsim/simulation"
)

// Este teste existe porque o χ² do resumo é uma AFIRMAÇÃO forte ("o sorteador segue
// λ(τ)"), e afirmação forte precisa de quem a verifique. Ele não testa o sorteador: ele
// testa o TESTE. Se o χ² estiver mal calibrado, ele acusa viés que é dele mesmo e a
// seção de aderência do resumo vira decoração — que é pior que não ter seção nenhuma.
//
// Ele já pagou o aluguel: pegou dois bugs de verdade. O grande foi a exposição somando
// FRAÇÃO DE DIA onde devia contar QUANTAS VEZES o run passou pelo bin — num run de 1.05
// dias os primeiros bins são varridos DUAS vezes e o esperado tem que dobrar, mas a
// conta dava 1.02x. Com `full=0` o erro era um fator constante que sumia na
// renormalização, e num horizonte de exatamente 1.0 dia `partial` era 0 e o erro nem
// aparecia — só quem estourava o dia sangrava. Ficou invisível nos dois casos fáceis.

// replica o laço do thinning sem motor nenhum: só a matemática, sem timer, sem canal.
func sampleHist(prof simulation.Profile, seed uint64, n int, dayS float64) domain.TrafficStat {
	scale := float64(n) / (prof.Norm() * dayS)
	lamMax := scale * prof.Sup()
	r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))

	var hist [domain.ProfileBins]int32
	var tm, tau float64
	for e := 0; e < n; e++ {
		for {
			tm += -math.Log(1-r.Float64()) / lamMax
			tau = math.Mod(tm/dayS, 1)
			if r.Float64()*lamMax < scale*prof.Density(tau)*prof.Norm() {
				break
			}
		}
		hist[int(tau*domain.ProfileBins)%domain.ProfileBins]++
	}
	return domain.TrafficStat{Hist: hist, SimSeconds: tm, DaySeconds: dayS}
}

// E[χ²] = gl. É a checagem mais direta que existe: se a média do χ² sobre muitas
// sementes destoar do gl, o teste está inflado (ou frouxo) e o p-valor não vale nada.
//
// Varre N crescente de propósito. Viés de casela pequena ENCOLHE com N; viés sistemático
// de forma CRESCE com N. Foi essa assinatura — razão subindo 1.125 -> 1.232 -> 1.439 —
// que apontou o dedo pra exposição, e não pra um artefato de borda.
func TestChiSquareNaoInfla(t *testing.T) {
	prof := simulation.DefaultProfile()
	const dayS = 1200.0
	const seeds = 400

	for _, n := range []int{300, 1000, 3000} {
		var sumChi, sumDf float64
		runs := 0
		for seed := uint64(1); seed <= seeds; seed++ {
			chi2, df, _, _ := ChiSquarePhase(prof, sampleHist(prof, seed, n, dayS))
			if df > 0 {
				sumChi += chi2
				sumDf += float64(df)
				runs++
			}
		}
		meanChi, meanDf := sumChi/float64(runs), sumDf/float64(runs)
		ratio := meanChi / meanDf
		// a folga aguenta o erro de Monte Carlo com 400 sementes (o desvio da média do χ²
		// é ~sqrt(2·gl/seeds) ≈ 0.48, uns 1% do gl) sem deixar passar viés de verdade —
		// o bug que isso pegou dava 1.44.
		if ratio < 0.93 || ratio > 1.07 {
			t.Errorf("N=%d: E[χ²]=%.1f com gl=%.1f (razão %.3f) — o teste está descalibrado, "+
				"o p-valor do resumo não vale", n, meanChi, meanDf, ratio)
		}
	}
}

// e a consequência do de cima, medida do lado de fora: sob a hipótese nula o p-valor tem
// que ser Uniforme(0,1). Então a fração de sementes que rejeita a 5% tem que ficar perto
// de 5% — um teste que rejeita 22% das próprias corridas boas está quebrado, e era
// exatamente esse o estado antes do conserto da exposição.
func TestPValorEhUniforme(t *testing.T) {
	prof := simulation.DefaultProfile()
	const dayS = 1200.0
	const seeds = 400

	var sum float64
	reject, runs := 0, 0
	for seed := uint64(1); seed <= seeds; seed++ {
		chi2, df, _, _ := ChiSquarePhase(prof, sampleHist(prof, seed, 300, dayS))
		p := ChiSquareP(chi2, df)
		if math.IsNaN(p) {
			continue
		}
		sum += p
		runs++
		if p < 0.05 {
			reject++
		}
	}
	mean := sum / float64(runs)
	rate := float64(reject) / float64(runs)
	if mean < 0.42 || mean > 0.58 {
		t.Errorf("média dos p = %.3f, esperado ~0.5 — a distribuição do p está torta", mean)
	}
	// teto generoso: com 400 sementes o próprio erro de amostragem da taxa é ~1.1pp.
	if rate > 0.11 {
		t.Errorf("rejeição a 5%% = %.1f%% em corridas legítimas, esperado ~5%% — falso positivo",
			100*rate)
	}
}

// gl<30 sai do alcance do Wilson–Hilferty, e aí a resposta certa é NÃO responder.
func TestPValorRecusaForaDaFaixa(t *testing.T) {
	if p := ChiSquareP(20, 12); !math.IsNaN(p) {
		t.Errorf("gl=12 devia devolver NaN em vez de um p fora da faixa; veio %v", p)
	}
	if p := ChiSquareP(47, 47); math.IsNaN(p) || p < 0.4 || p > 0.55 {
		t.Errorf("χ²=gl=47 devia dar p≈0.47; veio %v", p)
	}
}
