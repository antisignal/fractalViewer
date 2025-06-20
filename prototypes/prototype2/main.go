package main

import (
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"math"
)

/*
This second prototype will improve on the first in the following ways:
- inclusion of a way to select the bounds to display (given a center coordinate and a y half-length
- color
- zoom stack (left click zooms in by 2x, right click goes up a level)
- coordinate display (bounds, hover pos)

Things I'm not yet targeting include:
- support for multiple resolutions
- multithreading/anything parallel
*/

// these should not be changed from 640, 480 until the next iteration
const xRes = 640
const yRes = 480
const maxIterations = 1024

var mandelbrotRect = sdl.FRect{-2, -1.5, 4, 3}

func main() {
	var initFlags uint32 = sdl.INIT_EVERYTHING
	err := sdl.Init(initFlags)
	if err != nil {
		panic(err)
	}
	var _ *sdl.Window
	var renderer *sdl.Renderer
	_, renderer, err = sdl.CreateWindowAndRenderer(xRes, yRes, sdl.WINDOW_SHOWN)
	if err != nil {
		panic(err)
	}

	// outside of bounds x = 2/-2, y = 1.5/-1.5, nothing i want to display (this is actually very convenient)
	// drawing straight to the window

	err = renderer.SetDrawColor(255, 255, 255, 255)
	if err != nil {
		panic(err)
	}
	err = renderer.Clear()
	if err != nil {
		panic(err)
	}
	err = renderer.SetDrawColor(0, 0, 0, 255)

	// for the sake of making this a function: this entire section requires the mandelbrotRect and global variables as context...
	// actually, we can just make the mandelbrotRect a const and pass a scaled version of it

	// O(m*n) with kind of a high coefficient
	updateMandelbrotDisplay(mandelbrotRect, renderer)

	for {
		for e := sdl.PollEvent(); e != nil; e = sdl.PollEvent() {
			switch t := e.(type) {
			case *sdl.QuitEvent:
				sdl.Quit()
				break
			case *sdl.KeyboardEvent:
				if t.Keysym.Sym == sdl.K_ESCAPE {
					sdl.Quit()
					break
				}
				if t.Keysym.Sym == sdl.K_r {
					// resetZoomStack may not need to take any arguments - maybe we can make a newZoomStack instead
					masterZoomStack = resetZoomStack(masterZoomStack)
					updateMandelbrotDisplay(mandelbrotRect, renderer)
				}
			case *sdl.MouseButtonEvent:
				if t.Button == sdl.BUTTON_LEFT {
					if t.State == sdl.PRESSED {
						var mouseX, mouseY, _ = sdl.GetMouseState()
						scale, center := masterZoomStack.peek()
						var currentMandelbrotSubsetRect = genNextMandelbrotSubsetRectFromContext(mandelbrotRect, scale, center[0], center[1])
						var plotX, plotY = getPlotCoordsFromWindowCoordsAndContext(int(mouseX), int(mouseY), xRes, yRes, currentMandelbrotSubsetRect)
						masterZoomStack = masterZoomStack.push(float64(plotX), float64(plotY))
						var newScale = scale / 2
						var newMandelbrotSubsetRect = genNextMandelbrotSubsetRectFromContext(mandelbrotRect, newScale, plotX, plotY)
						updateMandelbrotDisplay(newMandelbrotSubsetRect, renderer)
					}
				}
			}
		}
	}
}

func genNextMandelbrotSubsetRectFromContext(mandelbrotSubsetRect sdl.FRect, scale float64, plotX float64, plotY float64) sdl.FRect {
	var scaledSubsetRect sdl.FRect = sdl.FRect{mandelbrotSubsetRect.X * float32(scale), mandelbrotSubsetRect.Y * float32(scale), mandelbrotSubsetRect.W * float32(scale), mandelbrotSubsetRect.H * float32(scale)}
	var transposedSubsetRect sdl.FRect = sdl.FRect{
		X: float32(plotX) - (scaledSubsetRect.W / 2),
		Y: float32(plotY) - (scaledSubsetRect.H / 2),
		W: scaledSubsetRect.W,
		H: scaledSubsetRect.H,
	}
	return transposedSubsetRect
	// var x1 = float32(plotX - (scale * float64(mandelbrotSubsetRect.W)))
	// var y1 = float32(plotY + (scale * float64(mandelbrotSubsetRect.H)))
	// // var x2 = plotX + (scale * ((xRes / 2) - 1))
	// // var y2 = plotY - (scale * ((yRes / 2) - 1))
	// return sdl.FRect{x1, y1, float32(float32(scale) * (mandelbrotSubsetRect.W / float32(xRes)) * float32(xRes)), float32(float32(scale) * (mandelbrotSubsetRect.H / float32(yRes)) * float32(yRes))} // check for off by 1 later
}

// unless this returns the array with the raw values, the values themselves are inaccessible from outside this function
func updateMandelbrotDisplay(mandelbrotSubsetRect sdl.FRect, renderer *sdl.Renderer) {
	for i := 0; i < xRes; i++ { // we'll iterate i,j for m,n - the actual coordinates
		for j := 0; j < yRes; j++ {
			var value = calculateMandelbrotValue(renderSurfaceToMandelbrotCoord([2]int{i, j}, mandelbrotSubsetRect, sdl.Rect{0, 0, xRes, yRes}), maxIterations)

			//var selectedColor = calculateColorFromValueViaCCG(value, 1024)
			populateColorFromValueBW(value, 1024)
			// for lack of a better name
			var selectedColorStore, _ = colorMemo[value]
			var selectedColor = &selectedColorStore
			err := renderer.SetDrawColor((*selectedColor).R, (*selectedColor).G, (*selectedColor).B, (*selectedColor).A)
			if err != nil {
				panic(err)
			}
			err = renderer.DrawPoint(int32(i), int32(j))
			if err != nil {
				panic(err)
			}
		}
	}
	renderer.Present()
}

func mandelbrotToRenderSurfaceCoord(inCoord complex128, mandelbrotRect sdl.FRect, renderSurfaceRect sdl.Rect) [2]int {
	// THIS FUNCTION DOES NOT WORK AS IS
	// still the inverse of renderSurfaceToMandelbrotCoord
	// this should take a rect of the plotted space and a rect of the screen resolution

	// as before
	var x = real(inCoord)
	var y = imag(inCoord)

	// the general idea here is to shift everything into the positive then scale it by the ratio along the x and y between the renderSurface and the mandelbrot plot area
	/* MIGHT USE LATER
	var xShiftFactor = -mandelbrotRect.X
	var yShiftFactor = -mandelbrotRect.y
	// no need to use renderSurfaceRect.X or .Y here since it's assumed to be 0
	var xScaleFactor = float32(renderSurfaceRect.W) / mandelbrotRect.W
	var yScaleFactor = float32(renderSurfaceRect.H) / mandelbrotRect.H
	*/
	// to maintain as much precision as possible, we wait to floor the values
	/* is there something I'm not seeing here? where would this function even be used?
	when we actually query a coordinate, we're starting from a pixel first */
	// putting aside for now

	x = (x + 2) * 160
	y = ((-y) + 1.5) * 160
	var outCoord = [2]int{int(math.Floor(x)), int(math.Floor(y))}
	return outCoord
}

func renderSurfaceToMandelbrotCoord(inCoord [2]int, mandelbrotRect sdl.FRect, renderSurfaceRect sdl.Rect) complex128 {
	// inverse of mandelbrotToRenderSurfaceCoord
	// this should take a rect of the plotted space and a rect of the screen resolution
	var x = float64(inCoord[0])
	var y = float64(inCoord[1])

	// we find the scale and shift factors again - maybe this can be passed as context?
	// these should be identical to the ones before
	var xShiftFactor = -mandelbrotRect.X
	var yShiftFactor = -mandelbrotRect.Y
	var xScaleFactor = float32(renderSurfaceRect.W) / mandelbrotRect.W
	var yScaleFactor = float32(renderSurfaceRect.H) / mandelbrotRect.H

	// worth considering: will all these type conversions cause issues later?
	x = (x / float64(xScaleFactor)) - float64(xShiftFactor)
	y = -((y / float64(yScaleFactor)) - float64(yShiftFactor))
	var outCoord = complex(x, y)
	return outCoord

	/*
		from previous iteration
		x = (x / 160) - 2
		y = -((y / 160) - 1.5)
		var outCoord = complex(x, y)
		return outCoord */
}

func individualCalculationsTest() {
	// test values
	fmt.Println(calculateMandelbrotValue(complex(0.2, 0.5), 1024))      // in the set
	fmt.Println(calculateMandelbrotValue(complex(-1.289, 0.031), 1024)) // in the set
	fmt.Println(calculateMandelbrotValue(complex(-1.272, 0.106), 1024)) // outside the set

	// sanity checking coord conversion functions
	/* fmt.Println(renderSurfaceToMandelbrotCoord(mandelbrotToRenderSurfaceCoord(complex(1, 1))))
	fmt.Println(renderSurfaceToMandelbrotCoord(mandelbrotToRenderSurfaceCoord(complex(2, 0))))
	fmt.Println(renderSurfaceToMandelbrotCoord(mandelbrotToRenderSurfaceCoord(complex(-2, -1)))) */
}

// passing maxIterations is probably redundant for now given that it's a global variable
func calculateMandelbrotValue(c complex128, maxIterations int) int {
	var z complex128 = complex(0, 0)
	for i := 0; i < maxIterations; i++ {
		// slight inefficiency
		var magnitude float64 = math.Sqrt((real(z) * real(z)) + (imag(z) * imag(z)))
		if magnitude > 2 {
			return i + 1 // starting with 1st iteration - unknown if this will cause problems
		}
		z = z*z + c
	}
	return -1 // -1 reserved for when, after max iterations, it does not diverge (since I'm pretty sure it can diverge instantly/leaving i=0
}

type zoomStackNode struct {
	scale  float64
	center [2]float64
	next   *zoomStackNode
}

type zoomStack struct {
	top *zoomStackNode
}

// one notable feature of this stack is that each next scale is half the previous, hence we don't add it as a parameter
// this requires the zoomStack to probably be seeded with a base coord and scale (1/160) (???)
// x and y are NOT the screen coords. there's some abuse of naming here. they're the coords on the plot
func (z zoomStack) push(x float64, y float64) zoomStack {
	var newTopNode = &zoomStackNode{
		scale:  z.top.scale / 2,
		center: [2]float64{x, y},
		next:   z.top, // this should be fine in every case
	}
	z.top = newTopNode
	return z
}

func (z zoomStack) pop() (scale float64, center [2]float64, updatedZoomStack zoomStack, ok bool) {
	var retNode = z.top
	if retNode != nil && retNode.next != nil {
		z.top = retNode.next
		return retNode.scale, retNode.center, z, true
	} else {
		return 0.0, [2]float64{0.0, 0.0}, z, false
	}
}

func (z zoomStack) peek() (scale float64, center [2]float64) {
	return z.top.scale, z.top.center
}

// should always be seeded

func resetZoomStack(z zoomStack) zoomStack {
	z.top = &zoomStackNode{
		// why would scale have been 1/160? what was my reasoning?
		scale:  1,
		center: [2]float64{0.0, 0.0},
	}
	return z
}

var masterZoomStack = zoomStack{
	top: &zoomStackNode{
		// why would scale have been 1/160? what was my reasoning?
		scale:  1,
		center: [2]float64{0.0, 0.0},
	},
}

// this function will be used a lot I anticipate. naming things is the hardest thing in programming
// xRes, yRes assumed to fit some constraints I haven't formalized
func getPlotCoordsFromWindowCoordsAndContext(windowX int, windowY int, xRes int, yRes int, mandelbrotSubsetRect sdl.FRect) (float64, float64) {
	// //var plotX = center[0] - ((float64(xRes-windowX) / 2.0) * scale)
	// // var plotY = center[1] - ((float64(yRes-windowY) / 2.0) * scale)
	// var plotX = center[0] - float64((xRes/2.0)-windowX)*scale*(float64(mandelbrotSubsetRect.W/float32(xRes)))
	// // 6/19 i changed this as a test - it was negative before. it might not work
	// var plotY = center[1] - float64((yRes/2.0)-windowY)*scale*(float64(mandelbrotSubsetRect.H/float32(yRes)))
	var fractOfMSRX = float64(windowX) / float64(xRes)
	var fractOfMSRY = float64(windowY) / float64(yRes)
	var plotX = (fractOfMSRX * float64(mandelbrotSubsetRect.W)) + float64(mandelbrotSubsetRect.X)
	var plotY = (fractOfMSRY * float64(mandelbrotSubsetRect.H)) + float64(mandelbrotSubsetRect.Y)
	return plotX, plotY
}

// colors
var gradientColorBlack = sdl.Color{0, 0, 0, 255}

// en.wikipedia.org/wiki/Color_gradient#/media/File:20180522_Color_palette_for_warming_stripes_-_ColorBrewer_9-class_single_hue.svg
// might give a false impression of white being the center
var gradientColors = [8]sdl.Color{
	sdl.Color{33, 113, 181, 255},  // color -6, range 0-127
	sdl.Color{107, 174, 214, 255}, // color -4, range 128-255
	sdl.Color{198, 219, 239, 255}, // color -2
	sdl.Color{255, 255, 255, 255}, // color 0
	sdl.Color{252, 187, 161, 255}, // color +2
	sdl.Color{251, 106, 74, 255},  // color +4
	sdl.Color{203, 24, 29, 255},   // color +6
	sdl.Color{103, 0, 13, 255},    // color +8
}

// this could maybe be improved with pointers to predefined colors - in fact, the colors already have to be predefined, so let's do this
func calculateColorFromValueViaCCG(val int, maxIterations int) *sdl.Color {
	if val == -1 {
		return &gradientColorBlack
	}
	var percentOfMaxIterations = float64(val) / float64(maxIterations)
	var colorChoice = int(math.Floor(percentOfMaxIterations * float64(len(gradientColors))))
	return &gradientColors[colorChoice]
}

var colorMemo = make(map[int]sdl.Color)

func populateColorFromValueBW(val int, maxIterations int) {
	// for now this has to be run every time - this is such a hack
	_, ok := colorMemo[val]
	if ok {
		return
	}
	var valAfterTransform int = int(math.Log2(float64(val)) * 32) // this is not clean but might work for now
	var colorComponentValues = uint8(255 * (float64(valAfterTransform) / float64(maxIterations)))
	var bWColor = sdl.Color{colorComponentValues, colorComponentValues, colorComponentValues, 255}
	colorMemo[val] = bWColor
}
