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

// Grade de tiles estilo Game Boy Advance (16px por tile).
const (
	TileSize = 16
	Cols     = 15 // 15 * 16 = 240 (largura GBA)
	Rows     = 11 // 11 * 16 = 176 (1 linha de HUD + 10 andares)

	GBWidth  = Cols * TileSize
	GBHeight = Rows * TileSize

	HUDHeight = TileSize

	waitAreaCol   = 2 // primeira coluna livre pra passageiros esperando
	waitAreaCols  = 5 // quantas colunas o "poço de espera" ocupa
	shaftBaseCol  = 8 // coluna do primeiro poço de elevador
	shaftColStep  = 2 // espaçamento entre poços
	maxPassengers = 8 // sprites desenhados antes de virar "+N"
)

// Paleta ampliada (tons de verde estilo GBA, com um acento âmbar pra portas/avisos).
var (
	Color0 = color.RGBA{0xe0, 0xf8, 0xd0, 0xff} // fundo claro
	Color1 = color.RGBA{0x88, 0xc0, 0x70, 0xff} // médio-claro
	Color2 = color.RGBA{0x34, 0x68, 0x56, 0xff} // médio-escuro
	Color3 = color.RGBA{0x08, 0x18, 0x20, 0xff} // escuro/contorno
	Accent = color.RGBA{0xf8, 0xc0, 0x50, 0xff} // âmbar, porta aberta / avisos
)

type Game struct {
	ctx       context.Context
	frames    <-chan ui.Frame
	lastFrame ui.Frame

	passImg  *ebiten.Image
	cabinImg *ebiten.Image

	cabinY [domain.NumCars]float64
}

func New(ctx context.Context, frames <-chan ui.Frame) (*Game, error) {
	g := &Game{
		ctx:    ctx,
		frames: frames,
	}

	g.passImg = newPassengerSprite()
	g.cabinImg = newCabinSprite()

	for i := 0; i < domain.NumCars; i++ {
		g.cabinY[i] = getFloorY(1)
	}
	return g, nil
}

// newPassengerSprite desenha um mini-personagem 8x8 (cabeça + corpo) em vez de um bloco liso.
func newPassengerSprite() *ebiten.Image {
	img := ebiten.NewImage(8, 8)
	// cabeça
	vector.DrawFilledRect(img, 2, 0, 4, 3, Color3, false)
	// corpo
	vector.DrawFilledRect(img, 1, 3, 6, 5, Color2, false)
	// realce da roupa
	vector.DrawFilledRect(img, 3, 4, 2, 3, Color3, false)
	return img
}

// newCabinSprite desenha uma cabine 16x16: teto com cabo, janela, porta e luz indicadora.
func newCabinSprite() *ebiten.Image {
	img := ebiten.NewImage(TileSize, TileSize)
	// cabo do teto
	vector.DrawFilledRect(img, 7, 0, 2, 2, Color3, false)
	// corpo da cabine
	vector.DrawFilledRect(img, 1, 2, 14, 13, Color3, false)
	// janela
	vector.DrawFilledRect(img, 3, 4, 10, 7, Color0, false)
	// trilho da porta ao centro da janela
	vector.DrawFilledRect(img, 7, 4, 2, 7, Color2, false)
	// luz indicadora no rodapé
	vector.DrawFilledRect(img, 6, 12, 4, 2, Accent, false)
	return img
}

func getFloorY(floor int) float64 {
	// andar 1 fica embaixo; HUDHeight reserva a barra de título no topo.
	return float64(HUDHeight + (domain.MaxFloor-floor)*TileSize)
}

func shaftX(car int) int {
	return (shaftBaseCol + car*shaftColStep) * TileSize
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
	for i := 0; i < domain.NumCars; i++ {
		targetY := getFloorY(g.lastFrame.Cars[i].Floor)

		diff := targetY - g.cabinY[i]
		if math.Abs(diff) > 0.5 {
			g.cabinY[i] += math.Copysign(0.75, diff)
		} else {
			g.cabinY[i] = targetY
		}
	}

	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(Color0)

	g.drawBuilding(screen)
	g.drawWaitingPassengers(screen)
	g.drawShafts(screen)
	g.drawCabins(screen)
	g.drawHUD(screen)
}

// drawBuilding desenha a estrutura do prédio com textura de laje (tijolo alternado) por andar.
func (g *Game) drawBuilding(screen *ebiten.Image) {
	for f := 1; f <= domain.MaxFloor; f++ {
		y := float32(getFloorY(f))

		// laje alternando tom claro/médio a cada andar, dando profundidade
		slab := Color0
		if f%2 == 0 {
			slab = color.RGBA{0xd4, 0xf0, 0xc4, 0xff}
		}
		vector.DrawFilledRect(screen, 0, y, float32(GBWidth), TileSize, slab, false)

		// linha do piso
		vector.DrawFilledRect(screen, 0, y+TileSize-1, float32(GBWidth), 1, Color1, false)

		// número do andar, dentro das duas primeiras colunas
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%2d", f), 4, int(y)+4)
	}
}

func (g *Game) drawWaitingPassengers(screen *ebiten.Image) {
	baseX := waitAreaCol * TileSize
	areaW := waitAreaCols * TileSize
	perRow := areaW / 8 // sprites de 8px cabem lado a lado

	for f := 1; f <= domain.MaxFloor; f++ {
		waiting := g.lastFrame.Waiting[f]
		if waiting <= 0 {
			continue
		}
		y := getFloorY(f) + (TileSize - 8) // alinha os passageiros na base do andar

		maxDrawn := int32(maxPassengers)
		if waiting < maxDrawn {
			maxDrawn = waiting
		}

		for p := int32(0); p < maxDrawn; p++ {
			row := p / int32(perRow)
			col := p % int32(perRow)
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Translate(float64(baseX)+float64(col*8), y-float64(row*8))
			screen.DrawImage(g.passImg, op)
		}

		if waiting > maxPassengers {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("+%d", waiting-maxPassengers), baseX, int(y)-16)
		}
	}
}

// drawShafts desenha os poços com trilhos laterais (parafusos) e cabo até o topo.
func (g *Game) drawShafts(screen *ebiten.Image) {
	for i := 0; i < domain.NumCars; i++ {
		x := float32(shaftX(i))
		vector.DrawFilledRect(screen, x, HUDHeight, TileSize, float32(GBHeight-HUDHeight), Color1, false)

		// trilhos (bordas do poço)
		vector.DrawFilledRect(screen, x, HUDHeight, 1, float32(GBHeight-HUDHeight), Color2, false)
		vector.DrawFilledRect(screen, x+TileSize-1, HUDHeight, 1, float32(GBHeight-HUDHeight), Color2, false)

		// cabo do elevador, do teto até a cabine atual
		vector.DrawFilledRect(screen, x+TileSize/2-1, HUDHeight, 2, float32(g.cabinY[i]-HUDHeight), Color3, false)
	}
}

func (g *Game) drawCabins(screen *ebiten.Image) {
	for i := 0; i < domain.NumCars; i++ {
		op := &ebiten.DrawImageOptions{}
		x := float64(shaftX(i))
		op.GeoM.Translate(x, g.cabinY[i])

		if g.lastFrame.Cars[i].State == domain.StateDoorsOpen {
			op.ColorScale.Scale(1.1, 1.0, 0.7, 1) // realça em âmbar quando a porta abre
		}

		screen.DrawImage(g.cabinImg, op)

		if g.lastFrame.Cars[i].Load > 0 {
			ebitenutil.DebugPrintAt(screen, fmt.Sprintf("%d", g.lastFrame.Cars[i].Load), int(x+4), int(g.cabinY[i])-10)
		}
	}
}

// drawHUD desenha a barra de título no topo, com contagem total de passageiros esperando.
func (g *Game) drawHUD(screen *ebiten.Image) {
	vector.DrawFilledRect(screen, 0, 0, float32(GBWidth), HUDHeight, Color3, false)
	vector.DrawFilledRect(screen, 0, HUDHeight-1, float32(GBWidth), 1, Accent, false)

	var totalWaiting int32
	for f := 1; f <= domain.MaxFloor; f++ {
		totalWaiting += g.lastFrame.Waiting[f]
	}

	ebitenutil.DebugPrintAt(screen, "ELEVATORSIM", 8, 4)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("WAIT:%2d", totalWaiting), GBWidth-72, 4)
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	// Ebiten escala essa resolução interna (240x176, grade GBA de 16px) pra janela sem borrar pixels.
	return GBWidth, GBHeight
}
