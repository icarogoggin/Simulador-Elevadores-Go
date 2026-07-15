package ui

import (
	"math"

	"elevatorsim/domain"
	"elevatorsim/simulation"
)

// confronta o histograma de fase OBSERVADO com o esperado sob λ(τ). É a única forma
// honesta de afirmar "é um Poisson não-homogêneo" num artefato: o programa testa o
// próprio sorteador contra a curva que ele diz estar seguindo. λ* baixo demais, sino
// cortado na volta do dia ou bug na normalização → o pico sai subamostrado e o p
// despenca.
//
// E_k = N·(massa do bin × exposição do bin), renormalizado pra ΣE = N. Como o laço do
// gerador para na N-ésima chegada, a contagem total é FIXA por construção: as caselas
// são MULTINOMIAIS, não Poissons independentes — que é exatamente o caso em que a
// estatística de Pearson vale, com gl = bins-1 (perde-se 1 grau pela contagem fixada).
//
// A exposição existe porque o run acaba em ~1.0 dia mas não em EXATAMENTE 1.0: os bins
// da volta do dia pegam um pedaço a mais, e sem corrigir isso o teste acusa um viés que
// é dele mesmo, não do sorteador.
// Devolve também quantas caselas ficaram com E<5 (small), porque é isso que decide se
// o teste vale: pela regra de Cochran a aproximação de Pearson aguenta E<5 em até ~20%
// das caselas desde que nenhuma fique abaixo de 1. Quem lê precisa dos dois números pra
// julgar, não só do mínimo — um E_min de 2.5 numa casela só é aceitável, em metade
// delas não é.
func ChiSquarePhase(p simulation.Profile, st domain.TrafficStat) (chi2 float64, df int, emin float64, small int) {
	var total float64
	for _, v := range st.Hist {
		total += float64(v)
	}
	if total == 0 || st.DaySeconds <= 0 {
		return 0, 0, 0, 0
	}
	days := st.SimSeconds / st.DaySeconds
	full := math.Floor(days)
	partial := days - full // fase final
	w := 1.0 / float64(domain.ProfileBins)

	// A ÚLTIMA CHEGADA SAI DA CONTA, e essa linha é a diferença entre um teste honesto e
	// um que rejeita sozinho. O laço para NA N-ésima chegada, então T_N não é um ponto
	// qualquer do eixo: é, por construção, uma chegada. Ela não é aleatória DADO T_N —
	// está lá com probabilidade 1 — e contá-la como se fosse infla o χ² sistematicamente.
	//
	// O resultado clássico: condicionado a T_N, as N-1 PRIMEIRAS chegadas são as
	// estatísticas de ordem de N-1 iid com densidade λ(s)/Λ(T_N) em [0,T_N]. Ou seja, o
	// multinomial vale pras N-1, com a exposição de [0,T_N] inteira. Então tiro UM evento
	// da casela onde T_N caiu — e não a casela toda, que jogaria fora a exposição dela
	// junto e não conserta nada.
	//
	// Medido com o sorteador rodando puro em 300 sementes: sem correção nenhuma a média
	// dos p dava 0.378 e 22% das corridas rejeitavam a 5%. Tirando a casela inteira,
	// 0.406/20% — quase não mexeu, era a correção errada. Horizonte FIXO (sem tempo de
	// parada) dá 0.506/6.3%, que é a prova de que a conta do χ² está certa e o viés era
	// todo do tempo de parada.
	stop := -1
	if partial > 0 {
		stop = int(partial / w)
		if stop >= domain.ProfileBins {
			stop = domain.ProfileBins - 1
		}
	}

	exp := make([]float64, domain.ProfileBins)
	obs := make([]float64, domain.ProfileBins)
	var sum, total2 float64
	for k := 0; k < domain.ProfileBins; k++ {
		lo, hi := float64(k)*w, float64(k+1)*w

		// esperado do bin = massa dele × quantas vezes o run passou por ali. Os dias
		// INTEIROS cobrem o bin completo, `full` vezes; o dia final cobre de lo até onde
		// o run parou, e essa parte eu integro DIRETO em vez de raspar uma fração da
		// massa total — dentro de um bin λ não é constante, e no sino do rush não é
		// constante nem de longe.
		e := full * p.MassIn(lo, hi)
		if partial > lo {
			e += p.MassIn(lo, math.Min(partial, hi))
		}
		exp[k] = e
		sum += e

		obs[k] = float64(st.Hist[k])
		if k == stop && obs[k] > 0 {
			obs[k]-- // a N-ésima chegada, a que definiu T_N
		}
		total2 += obs[k]
	}
	if sum <= 0 || total2 <= 0 {
		return 0, 0, 0, 0
	}
	total = total2 // o vínculo multinomial agora é o total das N-1
	emin = math.Inf(1)
	cells := 0
	for k, v := range obs {
		e := total * exp[k] / sum // renormaliza: ΣE = total, que é o vínculo multinomial
		if e <= 0 {
			// bin que o run nem chegou a viver (parou antes dessa fase): exposição zero,
			// esperado zero, observado zero. Não é aderência boa, é casela vazia — não
			// entra na conta E não conta grau de liberdade, senão eu inflava o gl com
			// células que não carregam informação nenhuma e o p saía otimista de graça.
			continue
		}
		cells++
		if e < 5 {
			small++
		}
		emin = math.Min(emin, e)
		d := v - e
		chi2 += d * d / e
	}
	if cells < 2 {
		return 0, 0, 0, 0
	}
	// perde-se 1 grau pela contagem total fixada (o vínculo multinomial).
	return chi2, cells - 1, emin, small
}

// p-valor por Wilson–Hilferty: (χ²/gl)^(1/3) é aproximadamente normal com média
// 1-2/(9gl) e variância 2/(9gl). Pra gl >= 30 (aqui é 47) o erro é de terceira casa — o
// bastante pra dizer "o sorteador bate com a curva" sem arrastar uma gama incompleta
// pra dentro do projeto.
func ChiSquareP(chi2 float64, df int) float64 {
	if df < 30 {
		return math.NaN() // fora da faixa da aproximação: melhor não imprimir do que mentir
	}
	n := float64(df)
	z := (math.Cbrt(chi2/n) - (1 - 2/(9*n))) / math.Sqrt(2/(9*n))
	return 0.5 * math.Erfc(z/math.Sqrt2)
}
