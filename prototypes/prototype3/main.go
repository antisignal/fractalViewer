package main

import (
	"encoding/json"
	"github.com/veandco/go-sdl2/sdl"
	"math"
	"os"
	"runtime"
	"sort"
)

/*
This will be the third iteration of the fractal viewer.

I would like to include, from before:
- gradient colors with 8 or 16 selections + black
- a zoom and position stack
- a HUD with info
-

As well as:
- parallel processing of the mandelbrot values (GPU?)
- loading settings from a file (.yaml or .json?)
  - custom color palette + initial bounds

- keyboard controls (cycling HUD modes)
- the HUD being multi-line, with an arbitrary amount of entries
- "go to" coordinate at scale -> requires keyboard input
- multiple fractal functions? (julia?)
- settings like HUD, fractal, etc. should just cycle for now

And eventually, in later iterations:
- arbitrary precision when the floating point math breaks down?

I suspect the following design decisions will help:
- composing the final image of "render layers" which have an order
- when render layers are updated, flag them as updated, then re-draw to the screen?
  - i want to avoid multiple concurrent event loops

7/2/2025: Soooo given that I can't mix the SDL.Renderer with raw OpenGL access, I'll have to just use goroutines
to speed up the fractal plot generation process. Using OpenCL was a bust and now OpenGL. Worst-case scenario at the
end of my work on this when I want to optimize it, I just drop into C and use OpenCL there, and figure out how to
interface with Go (maybe CGO.) That's gonna have to take a backseat.

Instead of doing a zoom stack, I'm going to have the mouse click with a modifier guide the new rect selection.
*/

/* 	general structure is as before: we render screens tied to a rect

 */

// should check, if there's for some reason a fractalViewer3 folder already there (why would there be though?),
// if the directory doesn't look like something this app created we'll just panic
// i'm excited to get to work on GUI error dialogs

const SettingsPathWindows = "%USERPROFILE%\\AppData\\Local\\fractalViewer3\\"
const SettingsPathNix = "$HOME/.fractalViewer3/"

type ColorMethod int

type ControlPointsColorPalette []ControlPointsPaletteEntry
type ModuloColorPalette []ModuloPaletteEntry

type ColorPalette struct {
	controlPointsColorPalette *ControlPointsColorPalette
	moduloColorPalette        *ModuloColorPalette
}

const (
	ColorMethodControlPoints = iota
	ColorMethodModulo        = iota
)

type ModuloPaletteEntry struct {
	color  *sdl.Color
	modulo int
}

type ControlPointsPaletteEntry struct {
	color        *sdl.Color
	controlPoint float32
}

func (c *ColorPalette) sortAndValidateColorPalette() bool {
	// validate
	if c.controlPointsColorPalette != nil {
		var seenInControlPointsPalette = make(map[float32]bool)
		for _, x := range *c.controlPointsColorPalette {
			_, ok := seenInControlPointsPalette[x.controlPoint]
			if ok {
				return false
			}
			seenInControlPointsPalette[x.controlPoint] = true
		}
	}

	if c.moduloColorPalette != nil {
		var lenMCP = len(*c.moduloColorPalette)
		var seenInModuloPalette = make(map[int]bool)
		for _, x := range *c.moduloColorPalette {
			if x.modulo > lenMCP-1 || x.modulo < 0 {
				return false
			}
			_, ok := seenInModuloPalette[x.modulo]
			if ok {
				return false
			}
			seenInModuloPalette[x.modulo] = true
		}
	}

	// sort
	if c.controlPointsColorPalette != nil {
		sort.Slice(c.controlPointsColorPalette, func(i, j int) bool {
			return (*c.controlPointsColorPalette)[i].controlPoint < (*c.controlPointsColorPalette)[j].controlPoint
		})
	}
	if c.moduloColorPalette != nil {
		sort.Slice(c.moduloColorPalette, func(i, j int) bool {
			return (*c.moduloColorPalette)[i].modulo < (*c.moduloColorPalette)[j].modulo
		})
	}
	return true
}

type SceneStack struct {
	scenes []*Scene
}

func (s *SceneStack) Push(scene *Scene, programContext *ProgramContext) {
	var top = s.Top()
	top.pause(top, programContext)
	scene.resume(scene, programContext)
	s.scenes = append(s.scenes, scene)
}
func (s *SceneStack) Pop(programContext *ProgramContext) *Scene {
	stackLen := len(s.scenes)
	if stackLen == 0 {
		return nil
	}
	var popped = s.scenes[stackLen-1]
	popped.pause(popped, programContext)
	s.scenes = s.scenes[:stackLen-2] // check for off by one here
	var newTop = s.Top()
	newTop.resume(newTop, programContext)
	return popped
}
func (s *SceneStack) Top() *Scene {
	stackLen := len(s.scenes)
	if stackLen == 0 {
		return nil
	}
	return s.scenes[stackLen-1]
}
func (s *SceneStack) Replace(newScene *Scene, programContext *ProgramContext) {
	var replaced = s.Top()
	replaced.pause(replaced, programContext)
	var scenesLen = len(s.scenes)
	s.scenes = s.scenes[:scenesLen-2]
	s.scenes = append(s.scenes, newScene)
	newScene.resume(newScene, programContext)
}

type Scene struct {
	update      func(*Scene, *ProgramContext)
	render      func(*Scene, *ProgramContext)
	handleEvent func(*Scene, *ProgramContext, *inputStateType)
	destroy     func(*Scene)
	pause       func(*Scene, *ProgramContext)
	resume      func(*Scene, *ProgramContext)

	data interface{}

	dataIsReady func(*Scene, *ProgramContext) bool
}

func validateScene(s *Scene) bool {
	if s.update == nil || s.render == nil || s.handleEvent == nil || s.destroy == nil {
		return false
	}
	if s.pause == nil || s.resume == nil {
		return false
	}
	return true
}

type Widget struct {
	X, Y, W, H int32
	parent     *Widget
	children   []*Widget

	data     interface{}
	initData func(*Widget, *ProgramContext)

	render      func(*Widget, *ProgramContext)
	handleEvent func(*Widget, *ProgramContext, *inputStateType)
	dataIsReady func(*Widget, *ProgramContext) bool
	dataIsSane  func(*Widget, *ProgramContext) bool // HACK: is there a meaningful distinction to be made here?
}

func (w *Widget) registerChildWidget(child *Widget) {
	w.children = append(w.children, child)
	child.parent = w
}

func getAbsolutePosition(w *Widget) [2]int32 {
	var relativePosition = [2]int32{w.X, w.Y}
	var parentAbsolutePosition [2]int32
	if w.parent != nil {
		parentAbsolutePosition = getAbsolutePosition(w.parent)
	} else {
		parentAbsolutePosition = [2]int32{} // (0,0)
	}
	var absolutePosition = [2]int32{
		relativePosition[0] + parentAbsolutePosition[0],
		relativePosition[1] + parentAbsolutePosition[1],
	}
	return absolutePosition
}

type rootWidgetData struct{}

type plotWidgetData struct {
	cachedTexture    *sdl.Texture
	cachedPlotValues *[]int16 // as a sanity check this should be [...].programSettings.
}

type ProgramContext struct {
	window          *sdl.Window
	renderer        *sdl.Renderer
	programSettings *ProgramSettings
}
type ProgramSettings struct {
	ColorPalette            *ColorPalette
	View                    *sdl.FRect
	WindowW                 int32
	WindowH                 int32
	TestRectangleColor      sdl.Color
	PlotResX                int32
	PlotResY                int32
	MaxMandelbrotIterations int16
	PreferredColorMethod    ColorMethod
}

func getInitProgramSettings() *ProgramSettings {
	return &ProgramSettings{
		// en.wikipedia.org/wiki/Color_gradient#/media/File:20180522_Color_palette_for_warming_stripes_-_ColorBrewer_9-class_single_hue.svg
		/* ColorPalette: []*sdl.Color{
			{33, 113, 181, 255},  // color -6, range 0-127
			{107, 174, 214, 255}, // color -4, range 128-255
			{198, 219, 239, 255}, // color -2
			{255, 255, 255, 255}, // color 0
			{252, 187, 161, 255}, // color +2
			{251, 106, 74, 255},  // color +4
			{203, 24, 29, 255},   // color +6
			{103, 0, 13, 255},    // color +8
		}, */
		// https://stackoverflow.com/questions/16500656/which-color-gradient-is-used-to-color-mandelbrot-in-wikipedia
		/* ColorPalette: &ColorPalette{
		&ControlPointsColorPalette{
			ControlPointsPaletteEntry{
				color:        &sdl.Color{0, 7, 100, 255},
				controlPoint: 0.0,
			},
			ControlPointsPaletteEntry{
				color:        &sdl.Color{32, 107, 203, 255},
				controlPoint: 0.16,
			},
			ControlPointsPaletteEntry{
				color: &sdl.Color{237,255,255},
			},
		}, [...] */
		ColorPalette: &ColorPalette{
			controlPointsColorPalette: nil,
			moduloColorPalette: &ModuloColorPalette{
				ModuloPaletteEntry{&sdl.Color{25, 7, 26, 255}, 1},
				ModuloPaletteEntry{&sdl.Color{9, 1, 47, 255}, 2},
				ModuloPaletteEntry{&sdl.Color{4, 4, 73, 255}, 3},
				ModuloPaletteEntry{&sdl.Color{0, 7, 100, 255}, 4},
				ModuloPaletteEntry{&sdl.Color{12, 44, 138, 255}, 5},
				ModuloPaletteEntry{&sdl.Color{24, 82, 177, 255}, 6},
				ModuloPaletteEntry{&sdl.Color{57, 125, 209, 255}, 7},
				ModuloPaletteEntry{&sdl.Color{134, 181, 229, 255}, 8},
				ModuloPaletteEntry{&sdl.Color{211, 236, 248, 255}, 9},
				ModuloPaletteEntry{&sdl.Color{241, 233, 191, 255}, 10},
				ModuloPaletteEntry{&sdl.Color{248, 201, 95, 255}, 11},
				ModuloPaletteEntry{&sdl.Color{255, 170, 0, 255}, 12},
				ModuloPaletteEntry{&sdl.Color{204, 128, 0, 255}, 13},
				ModuloPaletteEntry{&sdl.Color{153, 87, 0, 255}, 14},
				ModuloPaletteEntry{&sdl.Color{106, 52, 3, 255}, 15},
			},
		},
		PreferredColorMethod:    ColorMethodModulo,
		View:                    &sdl.FRect{-2, -1.5, 4, 3},
		WindowW:                 640,
		WindowH:                 480,
		TestRectangleColor:      sdl.Color{0, 0, 255, 255},
		PlotResX:                640,
		PlotResY:                480,
		MaxMandelbrotIterations: 1024,
	}
}

// config not human readable for now
// also dooooes this introduce any security concerns?
func saveProgramSettings(p *ProgramSettings) error {
	var settingsString, err = json.Marshal(*p)
	if err != nil {
		panic("failed to marshal json: " + err.Error())
	}

	if runtime.GOOS == "windows" {
		err := os.MkdirAll(SettingsPathWindows, 0755)
		if err != nil {
			panic(err) // this will need to be adjusted - a panic in this case is not very graceful
		}
		err = os.WriteFile(SettingsPathWindows+"config", settingsString, 0644)
		if err != nil {
			panic(err)
		}
	} else {
		// someone on stackoverflow said this was fine for all non-windows systems. there might be edge cases I
		// need to consider.
		err := os.MkdirAll(SettingsPathNix, 0755)
		if err != nil {
			panic(err)
		}
		err = os.WriteFile(SettingsPathNix+"config", settingsString, 0644)
		if err != nil {
			panic(err)
		}
	}
	return nil
}

func initSettingsFile() {
	err := saveProgramSettings(getInitProgramSettings())
	if err != nil {
		panic(err)
	}
}

func loadProgramSettings() (*ProgramSettings, error) {
	var loadedProgramSettingsString []byte
	if runtime.GOOS == "windows" {
		var err error
		// this works but I can't find it in windows explorer. i'll just roll with it for now...
		loadedProgramSettingsString, err = os.ReadFile(SettingsPathWindows + "config")
		if err != nil {
			// maybe create a new settings file instead?
			return nil, err
		}
	} else {
		// same issue as before with this maybe working for all non windows oses or maybe not
		var err error
		loadedProgramSettingsString, err = os.ReadFile(SettingsPathNix + "config")
		if err != nil {
			// maybe create a new settings file outside of this function?
			return nil, err
		}
	}
	loadedProgramSettings := &ProgramSettings{}
	err := json.Unmarshal(loadedProgramSettingsString, &loadedProgramSettings)
	if err != nil {
		panic(err)
	}
	return loadedProgramSettings, nil
}

func validateProgramContext(p *ProgramContext) bool {
	if p.window == nil || p.renderer == nil || p.programSettings == nil {
		return false
	}
	return true
}

const (
	keyMenu = iota
	numKeys = iota
)

const (
	mouseButtonLeft   = iota
	mouseButtonRight  = iota
	mouseButtonMiddle = iota
	numMouseButtons   = iota
)

const windowW = 640
const windowH = 480

type inputStateType struct {
	keys         [numKeys]bool
	mouseButtons [numMouseButtons]bool
}

func validateWidgetAndChildren(w *Widget) bool {
	// conditions not yet known
	if w.handleEvent == nil || w.render == nil {
		return false
	}
	for _, child := range w.children {
		if !validateWidgetAndChildren(child) {
			return false
		}
	}
	return true
}

type initSceneData struct {
	widgets []*Widget
}

func main() {
	// the OpenCL route i intended to go down is ineffective due to the (apparent) inadequacy of existing Go OpenCL
	// bindings. so instead I'm going to use either GLSL or a CPU/multi-goroutine based method

	// later, I will implement maybe two methods for calculating Mandelbrot plots: n-bit precision (using GLSL?) and
	// arbitrary precision (using a library, and calculating plot subsets in a multi-goroutine way)

	// for now:

	err := sdl.Init(sdl.INIT_EVERYTHING)
	if err != nil {
		panic(err)
	}
	defer sdl.Quit()
	window, renderer, err := sdl.CreateWindowAndRenderer(windowW, windowH, sdl.WINDOW_SHOWN)
	if err != nil {
		panic(err)
	}
	window.SetTitle("fractal viewer prototype 3!!! :DDD")
	// setup rootWidget
	var rootWidget = Widget{
		X: 0,
		Y: 0,
		W: windowW,
		H: windowH,

		data: &rootWidgetData{},

		parent:      nil,
		children:    []*Widget{},
		render:      nil,
		handleEvent: nil,
		dataIsReady: func(w *Widget, p *ProgramContext) bool {
			/* var wData, ok = w.data.(*rootWidgetData)
			// panic here or just return false?
			if !ok {
				panic("failed assertion: type assertion of rootWidget data failed!")
			}
			if wData.cachedTexture == nil {
				return false
			} */
			for _, child := range w.children {
				if !child.dataIsReady(child, p) {
					return false
				}
			}
			return true
		},
	}

	rootWidget.render = func(w *Widget, p *ProgramContext) {
		/* var data, ok = w.data.(*rootWidgetData)
		if !ok {
			panic("failed assertion: type assertion of rootWidget data failed!")
		}
		var tex = data.cachedTexture
		// on nil texture, panic instead of trying to continue. this probably means something's wrong
		if tex == nil {
			panic("failed to render rootWidget texture (nil reference and no fallback)")
		}
		// quick assert
		_, _, texW, texH, err := (*tex).Query()
		if err != nil {
			panic("failed to query rootWidget texture: " + err.Error())
		}
		if texW != rootWidget.W || texH != rootWidget.H {
			panic("failed assert: texture size does not match rootWidget size!")
		}
		err = p.renderer.Copy(tex, &sdl.Rect{W: w.W, H: w.H}, &sdl.Rect{X: w.X, Y: w.Y, W: w.W, H: w.H})
		if err != nil {
			panic("renderer failed to copy texture in rootWidget render: " + err.Error())
		}
		p.renderer.Present() */
		for _, child := range w.children {
			child.render(child, p)
		}
	}
	rootWidget.handleEvent = func(w *Widget, p *ProgramContext, state *inputStateType) {
		// asserts are assumed to pass, but it's possible to change the dimensions of the texture at runtime and I don't like that.
		// maybe there's a solution somewhere (7/2/25: should I have a dataSanityCheck function?)
		// placeholder
		/* data, ok := w.data.(*rootWidgetData)
		if !ok {
			panic("failed assert: type assertion of rootWidget data failed!")
		}
		 if data.cachedTexture == nil {
			data.cachedTexture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, w.W, w.H)
			err = p.renderer.SetRenderTarget(data.cachedTexture)
			if err != nil {
				panic(err)
			}
			defer func() {
				err = p.renderer.SetRenderTarget(nil)
				if err != nil {
					panic(err)
				}
			}()
			previousDrawColorR, previousDrawColorG, previousDrawColorB, previousDrawColorA, err := p.renderer.GetDrawColor()
			var settingsColor = p.programSettings.TestRectangleColor
			// err = p.renderer.SetDrawColor(255, 0, 0, 255)
			err = p.renderer.SetDrawColor(settingsColor.R, settingsColor.G, settingsColor.B, settingsColor.A)
			if err != nil {
				panic(err)
			}
			defer func() {
				err := p.renderer.SetDrawColor(previousDrawColorR, previousDrawColorG, previousDrawColorB, previousDrawColorA)
				if err != nil {
					return
				}
			}()
			err = p.renderer.Clear()
			if err != nil {
				panic(err)
			}
		} else {
			err = p.renderer.Copy(data.cachedTexture, &sdl.Rect{0, 0, w.W, w.H}, &sdl.Rect{w.X, w.Y, w.W, w.H})
		} */
		for _, child := range w.children {
			child.handleEvent(child, p, state)
		}
	}

	var plotWidget = Widget{
		// all coords of parent children are specified relative to parent. not implementing disown/adopt feature
		X: 0,
		Y: 0,
		W: rootWidget.W,
		H: rootWidget.H,

		parent:   &rootWidget,
		children: []*Widget{},

		data: &plotWidgetData{},
	}
	rootWidget.initData = func(w *Widget, p *ProgramContext) {
		for _, child := range w.children {
			child.initData(child, p)
		}
	}
	rootWidget.registerChildWidget(&plotWidget)
	plotWidget.render = func(w *Widget, p *ProgramContext) {
		// where we'll do the main mandelbrot plot rendering (from cache)

		if !w.dataIsReady(w, p) {
			panic("failed assert: plotWidget data accessed before ready")
		}
		if p.renderer.GetRenderTarget() != nil {
			panic("failed assert: render target not nil in plotWidget render!")
		}
		data, ok := w.data.(*plotWidgetData)
		if !ok {
			panic("failed assert: type assertion failed on plotWidget data!")
		}
		var tex = data.cachedTexture
		if tex == nil {
			panic("failed assert: texture used before created")
		}
		_, _, texW, texH, err := tex.Query()
		if err != nil {
			panic(err)
		}
		if texW != plotWidget.W || texH != plotWidget.H {
			panic("failed assert: plotWidget dimensions not the same as texture dimensions")
		}
		// copy from the bounds of the texture to the absolute rect
		var absolutePosition = getAbsolutePosition(w)

		err = p.renderer.Copy(tex, &sdl.Rect{
			0, 0, plotWidget.W, plotWidget.H}, &sdl.Rect{
			absolutePosition[0], absolutePosition[1], plotWidget.W, plotWidget.H})
		p.renderer.Present()
	}
	plotWidget.handleEvent = func(w *Widget, p *ProgramContext, i *inputStateType) {
		// for now, just regen the plot without taking events into account
		// i need to refactor this to have a dataIsReady() and dataIsSane()
		var data, ok = w.data.(*plotWidgetData)
		if !ok {
			panic("failed assert: type assertion failed on plotWidget data!")
		}
		if len(*data.cachedPlotValues) != int(p.programSettings.PlotResX)*int(p.programSettings.PlotResY) {
			panic("failed assert: len of cachedPlotValues does not match settings!")
		}
		data.cachedPlotValues = regenPlotValues(p)
		colorPlotWidgetTextureFromValues(w, p)
		w.render(w, p)
	}
	// should I do dataIsReady and dataIsSane?
	plotWidget.dataIsReady = func(w *Widget, p *ProgramContext) bool {
		// this assumes we don't need to check if the plot data is of the appropriate type before saying the data is ready
		if w.data == nil {
			return false
		}
		data, ok := w.data.(*plotWidgetData)
		if !ok {
			return false
		}
		if data.cachedTexture == nil {
			return false
		}
		// going for a flat array for performance reasons
		if len(*data.cachedPlotValues) != int(p.programSettings.PlotResX*p.programSettings.PlotResY) {
			panic("failed assert: len of cachedPlotValues doesn't match settings!")
		}
		return true
	}
	plotWidget.initData = func(w *Widget, p *ProgramContext) {
		var data, ok = w.data.(*plotWidgetData)
		if !ok {
			panic("failed assert: data not plotWidgetData in plotWidget.initData()")
		}
		if data.cachedTexture == nil {
			data.cachedTexture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, p.programSettings.PlotResX, p.programSettings.PlotResY)
			if err != nil {
				panic(err)
			}
		}
		if data.cachedPlotValues == nil {
			var blankPlotValues = make([]int16, p.programSettings.PlotResX*p.programSettings.PlotResY)
			data.cachedPlotValues = &blankPlotValues
		}
	}

	var programContext = ProgramContext{
		window:          window,
		renderer:        renderer,
		programSettings: getInitProgramSettings(),
	}

	rootWidget.initData(&rootWidget, &programContext)

	if !validateWidgetAndChildren(&rootWidget) {
		panic("failed assert: rootWidget failed to validate")
	}
	if !validateProgramContext(&programContext) {
		panic("failed assert: program context did not validate")
	}

	var inputState inputStateType = inputStateType{}

	// set up initScene
	var initScene = Scene{}

	initScene.data = &initSceneData{
		widgets: []*Widget{&rootWidget},
	}

	initScene.dataIsReady = func(s *Scene, p *ProgramContext) bool {
		var data, ok = s.data.(*initSceneData)
		if !ok {
			panic("failed assert: type assertion on initScene data failed!")
		}
		if data.widgets != nil {
			return true
		}
		for _, w := range data.widgets {
			if !w.dataIsReady(w, p) {
				return false
			}
		}
		return false
	}

	initScene.update = func(s *Scene, p *ProgramContext) {
		// pass
	}

	initScene.handleEvent = func(s *Scene, p *ProgramContext, i *inputStateType) {
		if !s.dataIsReady(s, p) {
			panic("failed assert: data not ready when initScene.update() called")
		}
		for _, w := range s.data.(*initSceneData).widgets {
			w.handleEvent(w, &programContext, i)
		}
	}

	initScene.render = func(s *Scene, p *ProgramContext) {
		if !s.dataIsReady(s, p) {
			panic("failed assert: data not ready when initScene.render() called")
		}
		for _, w := range s.data.(*initSceneData).widgets {
			w.render(w, p)
		}
	}
	initScene.destroy = func(s *Scene) {
		// let the GC handle it but make the program panic if it's used again (could be wasteful)
		// there's still a chance the data can be accessed after the fact...
		s.handleEvent = func(w *Scene, p *ProgramContext, state *inputStateType) {
			panic("failed assert: scene method called after destroy called (handleEvent)")
		}
		s.render = func(s *Scene, p *ProgramContext) {
			panic("failed assert: scene method called after destroy called (render)")
		}
		s.update = func(s *Scene, p *ProgramContext) {
			panic("failed assert: scene method called after destroy called (update)")
		}
		s.dataIsReady = func(s *Scene, p *ProgramContext) bool {
			panic("failed assert: scene method called after destroy called (dataIsReady)")
		}
		// uhh is this going to cause problems given I'm updating s.destroy() with s.destroy()?
		s.destroy = func(s *Scene) {
			panic("failed assert: scene method called after destroy called (destroy, ironically)")
		}
	}
	initScene.pause = func(s *Scene, p *ProgramContext) {}
	initScene.resume = func(s *Scene, p *ProgramContext) {}

	if !validateScene(&initScene) {
		panic("failed assert: failed to validate initScene")
	}

	// load settings
	programContext.programSettings, err = loadProgramSettings()
	if err != nil {
		programContext.programSettings = getInitProgramSettings()
		// save later, now that it works
		/*initSettingsFile()
		programContext.programSettings, err = loadProgramSettings()
		if err != nil {
			// maybe fall back to default settings without file?
			panic("could not create settings file: " + err.Error())
		} */
	}

	// skipping this test for now

	/* quick test
	err = saveProgramSettings(programContext.programSettings)
	if err != nil {
		panic(err)
	}
	loadedProgramSettings, err := loadProgramSettings()
	if err != nil {
		panic(err)
	}

	if *loadedProgramSettings != *programContext.programSettings {
		panic("failed assert: program settings change after save and load")
	} */

	// do the actual stuff we want to

	initScene.handleEvent(&initScene, &programContext, &inputState)
	initScene.render(&initScene, &programContext)

	// main event loop
	for e := sdl.PollEvent(); true; e = sdl.PollEvent() {
		if e != nil {
			handleInputEvent(&inputState, e)
		}
	}
}

// is using the same values that SDL uses a problem?
func handleInputEvent(inputState *inputStateType, e sdl.Event) {
	switch e.(type) {
	case *sdl.KeyboardEvent:
		var ev = e.(*sdl.KeyboardEvent)
		var state = false
		if ev.State == sdl.PRESSED {
			state = true
		}
		if ev.Keysym.Sym == sdl.K_m {
			if ev.State == sdl.PRESSED {
				inputState.keys[keyMenu] = state
			}
		}
	case *sdl.MouseButtonEvent:
		var ev = e.(*sdl.MouseButtonEvent)
		var state = false
		if ev.State == sdl.PRESSED {
			state = true
		}
		switch ev.Button {
		case sdl.BUTTON_LEFT:
			inputState.mouseButtons[mouseButtonLeft] = state
		case sdl.BUTTON_RIGHT:
			inputState.mouseButtons[mouseButtonRight] = state
		case sdl.BUTTON_MIDDLE:
			inputState.mouseButtons[mouseButtonMiddle] = state
		}
	case *sdl.QuitEvent:
		os.Exit(0)
	}
}

func regenPlotValues(p *ProgramContext) *[]int16 {
	// this might be expensive...
	var out = make([]int16, p.programSettings.PlotResX*p.programSettings.PlotResY)

	for i := range out {
		if i > math.MaxInt32 {
			// off by one maybe but it's inconsequential right now
			panic("failed assert: more than MaxInt32 + 1 values in plot!")
		}
		// y coord
		var quotient = int32(i) / p.programSettings.PlotResX
		var remainder = int32(i) % p.programSettings.PlotResX
		var currentPlotScreenCoord = [2]int32{remainder, quotient}
		var currentPlotPlaneCoord = convertPlotScreenCoordToPlotPlane(currentPlotScreenCoord, p)
		var currentPlotValue = calculatePlotValueFromPlaneCoord(currentPlotPlaneCoord, p)
		out[i] = currentPlotValue
	}
	return &out
}

// plotScreen coord is in the range 0-PlotResX, 0-PlotResY
// plotPlane coord is in the range PlotRangeRealLower-PlotRangeRealUpper, 0-PlotRangeImagLower, PlotRangeImageUpper
func convertPlotScreenCoordToPlotPlane(c [2]int32, p *ProgramContext) complex64 {
	// HACK: using sdl.FRect as our storage for float values limits our resolution to what float32 can support
	var screenCoordXProportionOfResX = float32(c[0]) / float32(p.programSettings.PlotResX)
	var screenCoordYProportionOfResY = float32(c[1]) / float32(p.programSettings.PlotResY)
	var planeCoordRealShift = screenCoordXProportionOfResX * p.programSettings.View.W
	var planeCoordImagShift = screenCoordYProportionOfResY * p.programSettings.View.H
	var planeCoordRealAbsolutePosition = p.programSettings.View.X + planeCoordRealShift
	var planeCoordImagAbsolutePosition = p.programSettings.View.Y + planeCoordImagShift
	return complex(planeCoordRealAbsolutePosition, planeCoordImagAbsolutePosition)
}

func calculatePlotValueFromPlaneCoord(c complex64, p *ProgramContext) int16 {
	// HACK: eventually we should replace this with a function that works for multiple fractals and continuous complex
	// functions
	// same as prototype 2
	var z complex128 = complex(0, 0)
	var i int16
	for i = 0; i < p.programSettings.MaxMandelbrotIterations; i++ {
		// slight inefficiency
		var magnitude float64 = math.Sqrt((real(z) * real(z)) + (imag(z) * imag(z)))
		if magnitude > 2 {
			return i + 1 // starting with 1st iteration - unknown if this will cause problems
		}
		z = z*z + complex128(c)
	}
	return -1 // -1 reserved for when, after max iterations, it does not diverge (since I'm pretty sure it can diverge instantly/leaving i=0
}

func colorPlotWidgetTextureFromValues(w *Widget, p *ProgramContext) {
	var initColor = [4]uint8{}
	var err error
	initColor[0], initColor[1], initColor[2], initColor[3], err = p.renderer.GetDrawColor()
	if err != nil {
		panic(err)
	}
	defer func() {
		err = p.renderer.SetDrawColor(initColor[0], initColor[1], initColor[2], initColor[3])
		if err != nil {
			panic(err)
		}
	}()

	var data, ok = w.data.(*plotWidgetData)
	if !ok {
		panic("failed assert: type assertion of widget data failed for widget passed to colorPlotWidget[...]()" +
			"(not a plotWidget?)")
	}
	if !w.dataIsReady(w, p) {
		panic("failed assert: data in colorPlotWidget[...]() not ready when used")
	}

	if int(p.programSettings.PlotResX*p.programSettings.PlotResY)-len(*data.cachedPlotValues) < 0 {
		panic("failed assert: last value of cachedPlotValues exceeds plot bounds per settings!")
	}

	err = p.renderer.SetRenderTarget(data.cachedTexture)
	if err != nil {
		panic(err)
	}
	defer func() {
		err = p.renderer.SetRenderTarget(nil)
		if err != nil {
			panic(err)
		}
	}()

	for i, x := range *data.cachedPlotValues {
		/* XXX: if the size of the cachedPlotValues can differ from the size of the plot texture (I can't remember if it
		does), when we try to index into it we'll end up with either this function writing to the top corner of the
		texture and leaving the rest unchanged (if cPV is smaller) or an out of bounds panic.
		there's an assert that these are supposed to be the same (screenCoord, planeCoord) but that's inconsistent with my intention
		for different values for the screenCoord res and planeCoord res. */
		// no assert necessary, but going to add it in case I change the int16 cachedPlotValues len to an int32
		if i > math.MaxInt32 {
			panic("failed assert: i greater than MaxInt32 in colorPlotWidget[...]()!")
		}
		var quotient = int32(i) / p.programSettings.PlotResX  // Y
		var remainder = int32(i) % p.programSettings.PlotResX // X
		// this assert can maybe just be done with the last value instead of tens of thousands of times...
		/* if quotient > p.programSettings.PlotResY-1 || remainder > p.programSettings.PlotResX-1 {
			panic("failed assert: quotient or remainder in colorPlotWidget[...]() exceeds screen bounds!")
		} */
		var color = getColorFromPlotValue(x, p)
		var colorDereferenced = *color
		err = p.renderer.SetDrawColor(colorDereferenced.R, colorDereferenced.G, colorDereferenced.B, colorDereferenced.A)
		err = p.renderer.DrawPoint(remainder, quotient)
	}
}

// HACK: this should be included in the palette as the non-escaping color.
var ColorBlack = sdl.Color{0, 0, 0, 255}

func getColorFromPlotValue(v int16, p *ProgramContext) *sdl.Color {
	var colorMethod = chooseColorMethod(p)

	if colorMethod == ColorMethodModulo {
		if v == -1 {
			return &ColorBlack
		}
		var lenMCP = len(*p.programSettings.ColorPalette.moduloColorPalette)
		var colorIndex = int(v) % lenMCP
		var mCP = p.programSettings.ColorPalette.moduloColorPalette
		return (*mCP)[colorIndex].color
	}
	if colorMethod == ColorMethodControlPoints {
		panic("control point coloring method not implemented yet")
	}
	panic("failed assert in getColorFromPlotValue(): chooseColorMethod didn't panic when it should have")

	/*
		var paletteLen = len(p.programSettings.ColorPalette)
		if paletteLen > math.MaxInt16 {
			panic("failed assert: too many colors in palette!!!")
		}
		if paletteLen == 0 {
			panic("failed assert: empty palette given!")
		}
		// using the same technique I used in prototype2

		_, ok := p.colorMemo */

	/*
		var paletteStep = p.programSettings.MaxMandelbrotIterations / int16(paletteLen)
		var paletteStepRemainder = p.programSettings.MaxMandelbrotIterations % int16(paletteLen)
		if paletteStepRemainder != 0 {
			panic("failed assert: max iterations not cleanly divisible by length of palette!")
		}
		var indexIntoPalette = int(v / paletteStep)
		return p.programSettings.ColorPalette[indexIntoPalette] */
}

func chooseColorMethod(p *ProgramContext) ColorMethod {
	if p.programSettings.ColorPalette.moduloColorPalette != nil {
		if p.programSettings.ColorPalette.moduloColorPalette == nil {
			return ColorMethodModulo
		}
		if p.programSettings.PreferredColorMethod == ColorMethodModulo {
			return ColorMethodModulo
		} else {
			return ColorMethodControlPoints
		}
	}
	if p.programSettings.ColorPalette.controlPointsColorPalette != nil {
		return ColorMethodControlPoints
	}
	panic("error: no color method available!! ;-;")
}
