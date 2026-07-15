package game

import (
	"context"
	"fmt"
	"image/color"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"elevatorsim/domain"
	"elevatorsim/ui"
)

// Resolução clássica do Game Boy
const (
	GBWidth      = 160
	GBHeight     = 144
	FloorHeight  = 13
	FloorOffsetY = 10
)

// Paleta clássica (Tons de Verde/Cinza)
var (
	Color0 = color.RGBA{0xe0, 0xf8, 0xd0, 0xff} // Mais claro (fundo)
	Color1 = color.RGBA{0x88, 0xc0, 0x70, 0xff} // Claro
	Color2 = color.RGBA{0x34, 0x68, 0x56, 0xff} // Escuro
	Color3 = color.RGBA{0x08, 0x18, 0x20, 0xff} // Mais escuro (bordas)
)

type Game struct {
	ctx        context.Context
	frames     <-chan ui.Frame
	lastFrame  ui.Frame
	
	passImg    *ebiten.Image
	cabinImg   *ebiten.Image
	
	cabinY     [domain.NumCars]float64
}

func New(ctx context.Context, frames <-chan ui.Frame) (*Game, error) {
	g := &Game{
		ctx:    ctx,
		frames: frames,
	}

	// Constrói o sprite do Passageiro pixel a pixel (3x5)
	g.passImg = ebiten.NewImage(3, 5)
	g.passImg.Fill(Color3)

	// Constrói o sprite do Elevador pixel a pixel (10x12)
	g.cabinImg = ebiten.NewImage(12, 12)
	g.cabinImg.Fill(Color3)
	// Janela do elevador
	vector.DrawFilledRect(g.cabinImg, 1, 1, 10, 10, Color0, false)
	// Barra da porta no meio
	vector.DrawFilledRect(g.cabinImg, 5, 1, 2, 10, Color2, false)

	// Inicializa os elevadores no chão (Andar 1)
	for i := 0; i < domain.NumCars; i++ {
		g.cabinY[i] = getFloorY(1)
	}
	return g, nil
}

func getFloorY(floor int) float64 {
	// floor 1 é embaixo. 
	// GBHeight - FloorOffsetY - (floor * FloorHeight)
	return float64(GBHeight - FloorOffsetY - (floor * FloorHeight))
}

func (g *Game) Update() error {
	select {
	case <-g.ctx.Done():
		return ebiten.Termination
	default:
	}

	for {
		select {
		case f := <-g.frames:
			g.lastFrame = f
		default:
			goto Interpolate
		}
	}

Interpolate:
	// Motor de física super simples para interpolar
	for i := 0; i < domain.NumCars; i++ {
		targetY := getFloorY(g.lastFrame.Cars[i].Floor)
		
		diff := targetY - g.cabinY[i]
		if math.Abs(diff) > 0.5 {
			// A cabine se move 0.5 pixels por frame
			g.cabinY[i] += math.Copysign(0.5, diff)
		} else {
			g.cabinY[i] = targetY
		}
	}

	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	// Limpa o fundo com a cor mais clara
	screen.Fill(Color0)

	// Desenha o Prédio (Linhas de andar)
	for f := 1; f <= domain.MaxFloor; f++ {
		y := getFloorY(f)
		// Linha do chão do andar
		vector.DrawFilledRect(screen, 10, float32(y+12), 140, 1, Color1, false)
		// Número do andar
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", f), 12, int(y))
	}

	// Desenha Passageiros
	for f := 1; f <= domain.MaxFloor; f++ {
		waiting := g.lastFrame.Waiting[f]
		if waiting > 0 {
			y := getFloorY(f)
			x := float64(22)
			
			// Para cada passageiro (até o limite de espaço, digamos 5)
			maxDrawn := int32(5)
			if waiting < maxDrawn {
				maxDrawn = waiting
			}
			
			for p := int32(0); p < maxDrawn; p++ {
				op := &ebiten.DrawImageOptions{}
				op.GeoM.Translate(x + float64(p*4), y+7)
				screen.DrawImage(g.passImg, op)
			}
			
			// Se tiver mais gente do que o desenhado, bota um numerozinho
			if waiting > 5 {
				ebitenutil.DebugPrintAt(screen, "+", int(x+22), int(y-2))
			}
		}
	}

	// Desenha Poços (Fundo do elevador)
	for i := 0; i < domain.NumCars; i++ {
		shaftX := float32(50 + (i * 30))
		vector.DrawFilledRect(screen, shaftX, 0, 14, float32(GBHeight), Color1, false)
	}

	// Desenha Cabines
	for i := 0; i < domain.NumCars; i++ {
		op := &ebiten.DrawImageOptions{}
		x := float64(51 + (i * 30))
		op.GeoM.Translate(x, g.cabinY[i])
		
		// Altera a cor se as portas estiverem abertas
		if g.lastFrame.Cars[i].State == domain.StateDoorsOpen {
			// Portas abertas, muda a cor do miolo usando ColorM
			op.ColorScale.Scale(0.5, 0.8, 0.5, 1) // Fica esverdeado escuro
		}
		
		screen.DrawImage(g.cabinImg, op)
		
		// Desenha a carga no topo do elevador
		if g.lastFrame.Cars[i].Load > 0 {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", g.lastFrame.Cars[i].Load), int(x+3), int(g.cabinY[i]-10))
		}
	}

	// Borda da tela
	vector.DrawFilledRect(screen, 0, 0, GBWidth, 10, Color3, false)
	vector.DrawFilledRect(screen, 0, GBHeight-10, GBWidth, 10, Color3, false)
	ebitenutil.DebugPrintAt(screen, " ELEVATORSIM ", 35, -2)
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	// Ebiten cuida de esticar essa resolução (160x144) para o tamanho da janela sem borrar os pixels!
	return GBWidth, GBHeight
}
