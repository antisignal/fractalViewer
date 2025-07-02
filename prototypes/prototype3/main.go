package main

import (
	"encoding/json"
	"github.com/veandco/go-sdl2/sdl"
	"os"
	"runtime"
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

	dataIsReady func(*Scene) bool
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

	data interface{}

	render      func(*Widget, *ProgramContext)
	handleEvent func(*Widget, *ProgramContext, *inputStateType)
	dataIsReady func(*Widget) bool
}

type rootWidgetData struct {
	cachedTexture *sdl.Texture
}

type ProgramContext struct {
	window          *sdl.Window
	renderer        *sdl.Renderer
	programSettings *ProgramSettings
}
type ProgramSettings struct {
	ColorPalette       []sdl.Color
	View               *sdl.FRect
	WindowW            int32
	WindowH            int32
	TestRectangleColor sdl.Color
}

func getInitProgramSettings() *ProgramSettings {
	return &ProgramSettings{
		ColorPalette:       []sdl.Color{},
		View:               &sdl.FRect{-2, -1.5, 4, 3},
		WindowW:            640,
		WindowH:            480,
		TestRectangleColor: sdl.Color{0, 0, 255, 255},
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

		data: &rootWidgetData{
			cachedTexture: nil,
		},

		parent:      nil,
		children:    []*Widget{},
		render:      nil,
		handleEvent: nil,
		dataIsReady: func(w *Widget) bool {
			var wData = w.data.(*rootWidgetData)
			if wData.cachedTexture == nil {
				return false
			}
			return true
		},
	}

	rootWidget.render = func(w *Widget, p *ProgramContext) {
		var tex = w.data.(*rootWidgetData).cachedTexture
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
		p.renderer.Present()
	}
	rootWidget.handleEvent = func(w *Widget, p *ProgramContext, state *inputStateType) {
		// asserts are assumed to pass, but it's possible to change the dimensions of the texture at runtime and I don't like that.
		// maybe there's a solution somewhere
		// placeholder
		data := w.data.(*rootWidgetData)
		if data.cachedTexture == nil {
			w.data.(*rootWidgetData).cachedTexture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, w.W, w.H)
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
			err = p.renderer.Copy(w.data.(*rootWidgetData).cachedTexture, &sdl.Rect{0, 0, w.W, w.H}, &sdl.Rect{w.X, w.Y, w.W, w.H})
		}
	}

	var programContext = ProgramContext{
		window:          window,
		renderer:        renderer,
		programSettings: getInitProgramSettings(),
	}

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

	initScene.dataIsReady = func(s *Scene) bool {
		if s.data.(*initSceneData).widgets != nil {
			return true
		}
		return false
	}

	initScene.update = func(s *Scene, p *ProgramContext) {
		// pass
	}

	initScene.handleEvent = func(s *Scene, p *ProgramContext, i *inputStateType) {
		if !s.dataIsReady(s) {
			panic("failed assert: data not ready when initScene.update() called")
		}
		for _, w := range s.data.(*initSceneData).widgets {
			w.handleEvent(w, &programContext, i)
		}
	}

	initScene.render = func(s *Scene, p *ProgramContext) {
		if !s.dataIsReady(s) {
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
		s.dataIsReady = func(s *Scene) bool {
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
		initSettingsFile()
		programContext.programSettings, err = loadProgramSettings()
		if err != nil {
			// maybe fall back to default settings without file?
			panic("could not create settings file: " + err.Error())
		}
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
