package main

import (
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"math"
)

/*
	This first prototype will implement a basic Mandelbrot visualization, with preset parameters.
	It will not have any GUI or zoom functionality, and is solely intended to display a Mandelbrot set at a given
	resolution in black and white.
	It will also calculate each pixel's color iteratively, without optimization, as a single frame.

*/

/*
	EXPECTATIONS:
	Going in, I expect I will need a function to determine the value of a single coordinate given Mandelbrot parameters.
	I also expect I will need a function to convert said value into a greyscale pixel value.

*/

// hardcoding is intentional for now since this will be iterated on quickly

func main() {
	var initFlags uint32 = sdl.INIT_EVERYTHING
	err := sdl.Init(initFlags)
	if err != nil {
		panic(err)
	}
	var _ *sdl.Window
	var renderer *sdl.Renderer
	_, renderer, err = sdl.CreateWindowAndRenderer(640, 480, sdl.WINDOW_SHOWN)
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

	// O(m*n) with kind of a high coefficient
	for i := 0; i < 640; i++ {
		for j := 0; j < 480; j++ {
			var value = calculateMandelbrotValue(renderSurfaceToMandelbrotCoord([2]int{i, j}), 1024)
			if value == -1 {
				err := renderer.DrawPoint(int32(i), int32(j))
				if err != nil {
					panic(err)
				}
			}
		}
	}
	renderer.Present()

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
			}
		}
	}
}

func mandelbrotToRenderSurfaceCoord(inCoord complex128) [2]int {
	// inverse of renderSurfaceToMandelbrotCoord
	var x = real(inCoord)
	var y = imag(inCoord)
	x = (x + 2) * 160
	y = ((-y) + 1.5) * 160
	var outCoord = [2]int{int(math.Floor(x)), int(math.Floor(y))}
	return outCoord
}

func renderSurfaceToMandelbrotCoord(inCoord [2]int) complex128 {
	// inverse of mandelbrotToRenderSurfaceCoord
	var x = float64(inCoord[0])
	var y = float64(inCoord[1])
	x = (x / 160) - 2
	y = -((y / 160) - 1.5)
	var outCoord = complex(x, y)
	return outCoord
}

func individualCalculationsTest() {
	// test values
	fmt.Println(calculateMandelbrotValue(complex(0.2, 0.5), 1024))      // in the set
	fmt.Println(calculateMandelbrotValue(complex(-1.289, 0.031), 1024)) // in the set
	fmt.Println(calculateMandelbrotValue(complex(-1.272, 0.106), 1024)) // outside the set

	// sanity checking coord conversion functions
	fmt.Println(renderSurfaceToMandelbrotCoord(mandelbrotToRenderSurfaceCoord(complex(1, 1))))
	fmt.Println(renderSurfaceToMandelbrotCoord(mandelbrotToRenderSurfaceCoord(complex(2, 0))))
	fmt.Println(renderSurfaceToMandelbrotCoord(mandelbrotToRenderSurfaceCoord(complex(-2, -1))))
}

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
