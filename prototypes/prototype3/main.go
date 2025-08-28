package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"

	"runtime/pprof"
	"sync"
)

/*
This will be the third iteration of the fractal viewer.

I would like to include, from before:
- gradient colors with 8 or 16 selections + black (done)
- a zoom and position stack (opted against)
- a HUD with info
-

As well as:
- parallel processing of the mandelbrot values (GPU?) (infeasible right now)
- loading settings from a file (.yaml or .json?) (done)
  - custom color palette + initial bounds (done)

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

I'm anticipating after getting the color to work that I'll need to have a Fractal interface that supports getPlot or
something.
*/

/* 	general structure is as before: we render screens tied to a rect

 */

// should check, if there's for some reason a fractalViewer3 folder already there (why would there be though?),
// if the directory doesn't look like something this app created we'll just panic
// i'm excited to get to work on GUI error dialogs

// 7/7/25: handling Intent should start with the Scene, and if the Scene can't handle it or needs to propagate it down it does

// 7/9/25: there's a lot of dead code and dead comments here. this iteration needs to be wrapped up, then I need to
// probably make a Kanban board for tasks related to the fractal viewer as well as a new requirements document

// this prototype will be done when the following tasks are completed:
// - basic GUI
//   - display information like Minecraft's F3 (?) menu (toggleable) (must do)
//     - display hover screenCoord, hover planeCoord, center planeCoord, zoom factor, value of pixel at cursor
//     - display performance metrics (time to generate plot, average gen time, other stats)
//     - display if floating point math is breaking down and suggest a switch to AP math at a performance cost (later)
//       - do test every regen vs. AP math to see if FP math is breaking down?
//   - controls display (toggleable)
// - reset view button
// - Fractal interface?
//   - getName(), getDefaultView(), getValueAtPlaneCoord() or getValuesForScreenDimensions(), etc?
//     - getValuesForScreenDimensions() takes the context, a rect, and an X/Y resolution and returns an array of pixels
//   - getColorMethod() which itself is an interface:
//     - colorPixel() (assumes independence of pixel colors)
//     - is it worth it to have a set of genColorMethod[Method]() function returning a function to reduce duplication? or is this too much?
//   - implement Julia fractal + others?
//   - change fractal via GUI? (next iteration)

// 08-29-25: this code needs the following revisions:
// - finish adding features
// - handle errors instead of panicking
// - clean up dead code and comments
// - fix HACK/XXX sections
// - strengthen validation
// - document once the prototype is complete
// - add unit tests

// future ambitions:
// - things listed at the top of this comment wall
// - save screenshot (overkill?)

// TODO: inconsistent exporting because I'm capitalizing the names of variables haphazardly at the moment

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
	if top != nil {
		top.pause(top, programContext)
	}
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
	s.scenes = s.scenes[:stackLen-1] // NOTE: fixed off by one here
	var newTop = s.Top()
	if newTop != nil {
		newTop.resume(newTop, programContext)
	}
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
	if replaced == nil {
		s.scenes = append(s.scenes, newScene)
		newScene.resume(newScene, programContext)
		return
	}
	replaced.pause(replaced, programContext)
	var scenesLen = len(s.scenes)
	s.scenes = s.scenes[:scenesLen-1] // check for off by one
	s.scenes = append(s.scenes, newScene)
	newScene.resume(newScene, programContext)
}

type Scene struct {
	update func(*Scene, *ProgramContext)
	render func(*Scene, *ProgramContext)
	// replaced with determineIntent/handleIntent
	// handleEvent func(*Scene, *ProgramContext, *inputStateType)
	destroy func(*Scene)
	pause   func(*Scene, *ProgramContext)
	resume  func(*Scene, *ProgramContext)

	data interface{}

	dataIsReady func(*Scene, *ProgramContext) bool

	determineIntent func(*Scene, *ProgramContext) Intent
	handleIntent    func(*Scene, *ProgramContext, Intent)

	// scenes should validateWidgetTree (?)
}

// QUESTIONABLE: does not validate scene data!!
func (s *Scene) validate() bool {
	if s.update == nil || s.render == nil || s.destroy == nil {
		return false
	}
	if s.pause == nil || s.resume == nil || s.determineIntent == nil || s.handleIntent == nil {
		return false
	}
	return true
}

type Widget struct {
	X, Y, W, H       int32
	kind             uint16
	parent           *Widget
	optionalChildren []*Widget

	data     interface{}
	initData func(*Widget, *ProgramContext)

	update func(*Widget, *ProgramContext)
	render func(*Widget, *ProgramContext)
	// replaced with handleIntent (intent determined by containing Scene)
	// handleEvent func(*Widget, *ProgramContext, *inputStateType)
	handleIntent func(*Widget, *ProgramContext, Intent)

	dataIsReady func(*Widget, *ProgramContext) bool
	dataIsSane  func(*Widget, *ProgramContext) bool // HACK: is there a meaningful distinction to be made here?
}

func (w *Widget) registerOptionalChildWidget(child *Widget) {
	w.optionalChildren = append(w.optionalChildren, child)
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

type GUIBaseWidgetData struct {
	hidden        bool
	cachedTexture *sdl.Texture
}

type GUIInfoTextListWidgetData struct {
}

// XXX: does baking the style into the widget defeat the purpose of my other design decisions? as in,
// should style be passed upon generating the widget while the widget itself remains agnostic to it?
// response: I'm moving forward with it left out
type GUIInfoTextWidgetData struct {
	correspondingInfoString InfoStringKind
	mandatoryChildren       GUIInfoTextWidgetMandatoryChildren
}

type GUITextWidgetData struct {
	text    string
	texture *sdl.Texture
}

type GUIInfoTextWidgetMandatoryChildren struct {
	textWidget  *Widget
	plateWidget *Widget
}

type GUIInfoTextListWidgetStyle struct {
	paddingBetweenEntries int
}

// TODO: should padding be pixels or proportion of the screen? or a hybrid method?
type GUIInfoTextWidgetStyle struct {
	innerPadding styleInfoInnerPadding
}

type GUIPlateWidgetStyle struct {
	innerPadding styleInfoInnerPadding
	plateColor   *sdl.Color
}

type GUITextWidgetStyle struct {
	font      *ttf.Font
	textColor *sdl.Color
}

type GUIPalette []sdl.Color

type styleInfoInnerPadding struct {
	paddingPixels     [4]uint32
	paddingProportion [4]float64
}

// must be populated for every type of widget
// TODO: have a const declaration with every type of widget, then have each widget instance have a field
// widgetType that stores the type of widget, so I can index into an array of widget styles?
// maybe also have a function which createsWidgetFromTypeWithoutChildren() or something which references
// the style array?
// this sounds like an iteration 4 thing
type StyleSheet struct {
	rootWidgetStyle            *rootWidgetStyle
	plotWidgetStyle            *plotWidgetStyle
	guiBaseWidgetStyle         *GUIBaseWidgetStyle
	guiInfoTextListWidgetStyle *GUIInfoTextListWidgetStyle
	guiInfoTextWidgetStyle     *GUIInfoTextWidgetStyle
	guiTextWidgetStyle         *GUITextWidgetStyle
	guiPlateWidgetStyle        *GUIPlateWidgetStyle
}

func (s *StyleSheet) initialize() *StyleSheet {
	s.rootWidgetStyle = &rootWidgetStyle{}
	s.plotWidgetStyle = &plotWidgetStyle{}
	s.guiBaseWidgetStyle = &GUIBaseWidgetStyle{}
	s.guiInfoTextListWidgetStyle = &GUIInfoTextListWidgetStyle{
		paddingBetweenEntries: 5,
	}
	s.guiInfoTextWidgetStyle = &GUIInfoTextWidgetStyle{
		innerPadding: styleInfoInnerPadding{
			paddingPixels:     [4]uint32{5, 5, 5, 5},
			paddingProportion: [4]float64{0., 0., 0., 0.},
		},
	}
	// this requires ttf to be init and a font to be available. it needs to be set in the main loop
	s.guiTextWidgetStyle = &GUITextWidgetStyle{
		textColor: &sdl.Color{R: 255, G: 255, A: 255},
		font:      nil,
	}
	s.guiPlateWidgetStyle = &GUIPlateWidgetStyle{
		plateColor: &sdl.Color{G: 255, B: 255, A: 255},
		innerPadding: styleInfoInnerPadding{
			paddingPixels:     [4]uint32{5, 5, 5, 5},
			paddingProportion: [4]float64{0, 0, 0, 0},
		},
	}
	return s
}

type rootWidgetStyle struct{}
type plotWidgetStyle struct{}
type GUIBaseWidgetStyle struct{}

// TODO: should the StyleSheet be part of the programSettings? (yes!)
type ProgramContext struct {
	window           *sdl.Window
	renderer         *sdl.Renderer
	programSettings  *ProgramSettings
	sceneStack       *SceneStack
	inputHistory     *InputHistoryType
	programInfoCache *ProgramInfoCache
}

type ProgramInfoCache struct {
	infoStrings         [numInfoStringKinds]InfoString
	timesToGeneratePlot []uint64
	lastKnownHoverCoord [2]uint32
}

type InfoString struct {
	infoStringKind InfoStringKind
	string         string
}

type InfoStringKind uint8

// HACK: should I be using reflect here to make sure the settings are valid?
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
	UnitView                *sdl.FRect
	StyleSheet              *StyleSheet
	DefaultFontSize         int
	DefaultFontLocation     string
}

// initialize shouldn't require any external anything to get an initialized copy
type Initializable[T any] interface {
	initialize() T
}

// for some things we can validate the instance with it alone,
// for some things it needs to reflect the ProgramContext or some other outside information
// so far, validation only requires itself
type Validatable interface {
	validate() bool
}

// what's the point of getting a pointer to it if i'm just gonna overwrite it?
func (p *ProgramSettings) initialize() *ProgramSettings {
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
		// HACK: (?) is this (unitview and view having separate heap objects) wasteful? does it even matter?
		PreferredColorMethod:    ColorMethodModulo,
		UnitView:                &sdl.FRect{-2, -1.5, 4, 3},
		View:                    &sdl.FRect{-2, -1.5, 4, 3},
		WindowW:                 640,
		WindowH:                 480,
		TestRectangleColor:      sdl.Color{0, 0, 255, 255},
		PlotResX:                640,
		PlotResY:                480,
		MaxMandelbrotIterations: 1024,
		DefaultFontLocation:     "../prototype2/m5x7.ttf",
		DefaultFontSize:         16,
		StyleSheet: &StyleSheet{
			rootWidgetStyle:            &rootWidgetStyle{},
			plotWidgetStyle:            &plotWidgetStyle{},
			guiBaseWidgetStyle:         &GUIBaseWidgetStyle{},
			guiInfoTextListWidgetStyle: &GUIInfoTextListWidgetStyle{},
			guiInfoTextWidgetStyle:     &GUIInfoTextWidgetStyle{},
			guiTextWidgetStyle: &GUITextWidgetStyle{
				textColor: &sdl.Color{255, 255, 255, 255},
			},
			guiPlateWidgetStyle: &GUIPlateWidgetStyle{
				innerPadding: styleInfoInnerPadding{
					paddingPixels: [4]uint32{5, 5, 5, 5},
				},
				plateColor: &sdl.Color{0, 0, 0, 255},
			},
		},
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

func createDefaultSettingsFile() {
	err := saveProgramSettings((&ProgramSettings{}).initialize())
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

func (p *ProgramContext) validate() bool {
	if p.window == nil || p.renderer == nil || p.programSettings == nil {
		return false
	}
	if p.sceneStack == nil {
		return false
	}
	if p.inputHistory == nil {
		return false
	}
	if p.programInfoCache == nil {
		return false
	}
	if !p.programSettings.validate() {
		return false
	}
	/* if p.programSettings.View == nil || p.programSettings.UnitView == nil || p.programSettings.ColorPalette == nil {
		return false
	} */
	return true
}

func (p *ProgramSettings) validate() bool {
	if p.View == nil || p.UnitView == nil || p.ColorPalette == nil {
		return false
	}
	// TODO: add more things here
	if p.DefaultFontLocation == "" || p.DefaultFontSize == 0 {
		return false
	}
	if p.StyleSheet == nil {
		return false
	}
	if !p.StyleSheet.validate() {
		return false
	}
	return true
}

func (s *StyleSheet) validate() bool {
	if s.rootWidgetStyle == nil || s.plotWidgetStyle == nil || s.guiInfoTextWidgetStyle == nil {
		return false
	}
	if s.guiInfoTextListWidgetStyle == nil || s.guiBaseWidgetStyle == nil {
		return false
	}
	if s.guiPlateWidgetStyle == nil || s.guiTextWidgetStyle == nil {
		return false
	}

	if s.guiTextWidgetStyle.font == nil {
		return false
	}
	return true
}

const (
	keyMenu      = iota
	keyZoom      = iota
	keyResetView = iota
	numKeys      = iota
)

const (
	mouseButtonLeft   = iota
	mouseButtonRight  = iota
	mouseButtonMiddle = iota
	numMouseButtons   = iota
)

const windowW = 640
const windowH = 480

const defaultZoomFactor = 0.5

const (
	GUIPaletteLightColor  = iota
	GUIPaletteDarkColor   = iota
	GUIPaletteAccentColor = iota
	numGUIPaletteItems    = iota
)

// check: does this fragment anything, or add redundancy in an unhelpful way?
const (
	rootWidgetKind            = iota
	plotWidgetKind            = iota
	guiBaseWidgetKind         = iota
	guiInfoTextListWidgetKind = iota
	guiInfoTextWidgetKind     = iota
	guiPlateWidgetKind        = iota
	guiTextWidgetKind         = iota
	numWidgetKinds            = iota
)

const (
	infoStringKindHoverScreenCoord           = iota
	infoStringKindHoverPlaneCoord            = iota
	infoStringKindInstantaneousTimeToDisplay = iota
	infoStringKindAverageTimeToDisplay       = iota
	numInfoStringKinds                       = iota
)

const (
	paddingLeft   = iota
	paddingTop    = iota
	paddingRight  = iota
	paddingBottom = iota
)

type inputStateType struct {
	keys         [numKeys]buttonState
	mouseButtons [numMouseButtons]buttonState
	mouseXY      [2]uint32
	timestamp    uint64
}

type buttonState struct {
	pressed  bool
	held     bool
	released bool
}

func (data *GUIInfoTextListWidgetData) validate() bool {
	return true
}

// pass for now
func (i *inputStateType) initialize() *inputStateType {
	i.timestamp = math.MaxUint64
	return i
}

// pass for now
func (i *inputStateType) validate() bool {
	if i.timestamp == 0 {
		// log this probably shouldn't happen
		log.Default().Println("WARNING: inputStateType variable has timestamp of 0. this hasn't been ruled out but is unexpected")
	}
	return true
}

// should this be a []iST or []*iST?
// inputHistory needs entries to be immutable, but history itself to be changeable
type InputHistoryType struct {
	inputs []*inputStateType
}

// pass for now
func (i *InputHistoryType) initialize() *InputHistoryType {
	return i
}

// we have intents which contain a type and parameters, which are an interface

type Intent struct {
	intentType       uint8
	intentParameters IntentParameters
} // was a uint8

func (i *Intent) validate() bool {
	switch i.intentType {
	case IntentChangeCenter:
		params, ok := i.intentParameters.(*IntentParametersChangeCenter)
		if !ok {
			panic("failed assert: assertion of type intentParametersChangeCenter failed in intent validation!")
		}
		return params.validate()
	case IntentZoom:
		params, ok := i.intentParameters.(*IntentParametersZoom)
		if !ok {
			panic("failed assert: assertion of type intentParametersZoom failed in intent validation!")
		}
		return params.validate()
	default:
		panic("failed assert: trying to validate intent, but no case in validator for intentType!")
	}
}

func (iP IntentParametersChangeCenter) validate() bool {
	// maybe do stuff here later
	return true
}

func (iP IntentParametersZoom) validate() bool {
	// maybe do stuff here later
	if iP.factor < 0 {
		return false
	}
	return true
}

type IntentParameters interface {
	Validatable
}

type IntentParametersChangeCenter struct {
	newCenter complex64
}
type IntentParametersZoom struct {
	factor float64
}
type IntentParametersMoveMouse struct {
	targetScreenCoord [2]uint32
}
type IntentParametersResetView struct{}
type IntentParametersStart struct{}
type IntentParametersUnknown struct{}
type IntentParametersToggleGUIInfoTextList struct{}

func (iP IntentParametersStart) validate() bool {
	return true
}
func (iP IntentParametersUnknown) validate() bool {
	return true
}
func (iP IntentParametersResetView) validate() bool             { return true }
func (iP IntentParametersMoveMouse) validate() bool             { return true }
func (iP IntentParametersToggleGUIInfoTextList) validate() bool { return true }

const (
	IntentExit                  = iota
	IntentStart                 = iota
	IntentUnknown               = iota
	IntentChangeCenter          = iota
	IntentZoom                  = iota
	IntentResetView             = iota
	IntentMoveMouse             = iota
	IntentToggleGUIInfoTextList = iota
	numIntents                  = iota
)

func (w *Widget) validate() bool {
	// conditions not yet known
	// if dataIsSane == nil
	if w == nil || w.render == nil || w.handleIntent == nil {
		return false
	}
	if w.dataIsReady == nil {
		return false
	}
	if w.update == nil {
		return false
	}
	for _, child := range w.optionalChildren {
		if !child.validate() {
			return false
		}
	}
	return true
}

type initSceneData struct {
	widgets []*Widget
}

func getFreshRootWidget() *Widget {
	var rootWidget = &Widget{
		X: 0,
		Y: 0,
		W: windowW,
		H: windowH,

		data: &rootWidgetData{},

		parent:           nil,
		optionalChildren: []*Widget{},
		render:           nil,
		dataIsReady: func(w *Widget, p *ProgramContext) bool {
			/* var wData, ok = w.data.(*rootWidgetData)
			// panic here or just return false?
			if !ok {
				panic("failed assertion: type assertion of rootWidget data failed!")
			}
			if wData.cachedTexture == nil {
				return false
			} */
			for _, child := range w.optionalChildren {
				if !child.dataIsReady(child, p) {
					return false
				}
			}
			return true
		},
		handleIntent: func(w *Widget, p *ProgramContext, i Intent) {},
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
		for _, child := range w.optionalChildren {
			child.render(child, p)
		}
	}
	rootWidget.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {
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

		// w.update(w, p) // commented out as an experiment on 2025-08-28
		switch i.intentType {
		case IntentMoveMouse:
			for _, child := range w.optionalChildren {
				if child.kind == guiBaseWidgetKind {
					child.handleIntent(child, p, i)
				}
			}
			return
		}
		for _, child := range w.optionalChildren {
			if child.kind == guiBaseWidgetKind {
				child.handleIntent(child, p, i)
			}
		}
	}

	rootWidget.update = func(w *Widget, p *ProgramContext) {
		for _, child := range w.optionalChildren {
			child.update(child, p)
		}
	}
	rootWidget.initData = func(w *Widget, p *ProgramContext) {
		for _, child := range w.optionalChildren {
			child.initData(child, p)
		}
	}
	rootWidget.kind = rootWidgetKind
	return rootWidget
}

// TODO: we should be able to have a plot widget attach to a parentWidget that isn't a rootWidget
func getFreshPlotWidget(rootWidget *Widget) *Widget {

	// if we're sloppy this might not even guarantee that it's a root widget for sure, but it helps
	if rootWidget.kind != rootWidgetKind {
		panic("failed assert: widget passed as rootWidget to getFreshPlotWidget() not of kind rootWidgetKind")
	}
	// double checking out of paranoia ;)
	_, ok := rootWidget.data.(*rootWidgetData)
	if !ok {
		panic("failed assert: even though the rootWidget passed to getFreshPlotWidget() is supposedly of kind" +
			"rootWidgetKind, its data doesn't pass the type assertion!!")
	}

	var plotWidget = &Widget{
		// all coords of parent optionalChildren are specified relative to parent. not implementing disown/adopt feature
		X: 0,
		Y: 0,
		W: rootWidget.W,
		H: rootWidget.H,

		parent:           rootWidget,
		optionalChildren: []*Widget{},

		kind: plotWidgetKind,

		data: &plotWidgetData{},
	}
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
	// XXX: i'm pretty sure I have no way of marking a click event as handled here. this needs to be looked at later.
	plotWidget.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {
		// for now, just regen the plot without taking events into account
		// i need to refactor this to have a dataIsReady() and dataIsSane()
		// this assumes that handleIntent() is called right after determineIntent() and
		// before any further input is registered
		switch i.intentType {
		case IntentStart:
			w.update(w, p)
		default:
			panic("unhandled default case in plotWidget.handleIntent")
		}

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
			var err error
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
	plotWidget.update = func(w *Widget, p *ProgramContext) {
		var data, ok = w.data.(*plotWidgetData)
		if !ok {
			panic("failed assert: type assertion failed on plotWidget data!")
		}
		if len(*data.cachedPlotValues) != int(p.programSettings.PlotResX)*int(p.programSettings.PlotResY) {
			panic("failed assert: len of cachedPlotValues does not match settings!")
		}

		// deprecated because we use Intents now
		/* var currentInputState = p.inputHistory.top()
		if currentInputState.mouseButtons[mouseButtonLeft] {
			var mouseX, mouseY, _ = sdl.GetMouseState()
			var coord = convertPlotScreenCoordToPlotPlane([2]int32{mouseX, mouseY}, p)
			changePlotView(p, 1, coord)
		} */

		data.cachedPlotValues = regenPlotValues(p)
		colorPlotWidgetTextureFromValues(w, p)
		// w.render(w, p) COMMENTED AS A TEST
	}
	return plotWidget
}

func getFreshGUIBaseWidget(rootWidget *Widget) *Widget {
	var GUIBaseWidget = &Widget{
		X: 0,
		Y: 0,
		W: rootWidget.W,
		H: rootWidget.H,

		parent:           rootWidget,
		optionalChildren: []*Widget{},

		data: &GUIBaseWidgetData{},
	}

	GUIBaseWidget.render = func(w *Widget, p *ProgramContext) {
		if !w.dataIsReady(w, p) {
			panic("failed assert: data not ready when rendering GUIBaseWidget!")
		}
		var data = w.data.(*GUIBaseWidgetData)
		if !data.hidden {
			for _, c := range w.optionalChildren {
				c.render(c, p)
			}
		}
	}

	GUIBaseWidget.update = func(w *Widget, p *ProgramContext) {
		for _, c := range w.optionalChildren {
			c.update(c, p)
		}
	}
	GUIBaseWidget.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {
		switch i.intentType {
		case IntentMoveMouse:
			for _, c := range w.optionalChildren {
				if c.kind != plotWidgetKind {
					c.update(c, p)
				}
			}
		case IntentStart:
			return
		default:
			panic("unhandled default case in GUIBaseWidget.handleIntent")
		}
	}
	GUIBaseWidget.initData = func(w *Widget, p *ProgramContext) {
	}
	GUIBaseWidget.dataIsReady = func(w *Widget, p *ProgramContext) bool {
		/* var data, ok = w.data.(*GUIBaseWidgetData)
		if !ok {
			panic("failed assert: type assert in GUIBaseWidget.dataIsReady failed (on data!)")
		}
		if data.cachedTexture == nil {
			return false
		} */
		return true
	}
	GUIBaseWidget.dataIsSane = func(w *Widget, p *ProgramContext) bool { return true }
	GUIBaseWidget.kind = guiBaseWidgetKind
	return GUIBaseWidget
}

func getFreshGUIInfoTextListWIdget(guiBaseWidget *Widget) *Widget {
	var freshGUIInfoTextListWidget = &Widget{
		X: guiBaseWidget.X,
		Y: guiBaseWidget.Y,
		W: guiBaseWidget.W,
		H: guiBaseWidget.H,
	}
	freshGUIInfoTextListWidget.update = func(w *Widget, p *ProgramContext) {
		// when we update, we get each of the infoStrings
		// => generate the text
		// => generate the plate (based on text rect + parameters for inner padding on the plate)
		// => position the plate and text relative to widget (based on inner padding of the plate + inner padding of the widget
		// => render the widget (which just renders the plate, then the text -
		// each of their rendering functions is just copying their cached texture (from their data field) to
		// their absolute position

		// my other thought process: destroy all the children (diabolical), regen the text, regen the plate,
		// set the offsets, determine position of self relative to base widget, render the plate at its absolute position,
		// render the text at its absolute position,

		// doing this without a regen might be more complex?

		// this is unnecessary because I forgot the infotextwidgets are stored as optional children
		var data, ok = w.data.(*GUIInfoTextListWidgetData)
		if !ok {
			panic("failed assert: type assertion failed on data in freshGUIInfoTextListWidget.update()!")
		}
		if !data.validate() {
			panic("failed assert: data in freshGUIInfoTextListWidget.update() failed to validate!")
		}

		for i, c := range w.optionalChildren {
			// this should include a flag for the corresponding infoString
			var childData = c.data.(*GUIInfoTextWidgetData)
			// this should generate a texture based on the corresponding infoString
			childData.mandatoryChildren.textWidget.update(w, p)
			// then we generate the plate - we assume (is this good practice?) that we're part of a GUIInfoTextWidget
			// and get the text widget via the parent
			childData.mandatoryChildren.plateWidget.update(w, p) // this should also update the size of the plate widget
			// now we set the positions of each based on the StyleSheet
			// TODO: make this also take paddingProportion into account
			// XXX: conversion from uint32 to int32 (should not matter, but needs review later)

			// XXX: check this
			childData.mandatoryChildren.plateWidget.X = w.W -
				(int32(p.programSettings.StyleSheet.guiInfoTextWidgetStyle.innerPadding.paddingPixels[paddingLeft]) +
					(int32(p.programSettings.StyleSheet.guiInfoTextWidgetStyle.innerPadding.paddingPixels[paddingRight]) +
						childData.mandatoryChildren.plateWidget.W))
			childData.mandatoryChildren.plateWidget.Y = 0 +
				((int32(p.programSettings.StyleSheet.guiInfoTextWidgetStyle.innerPadding.paddingPixels[paddingTop]) +
					(int32(p.programSettings.StyleSheet.guiInfoTextWidgetStyle.innerPadding.paddingPixels[paddingBottom]) +
						childData.mandatoryChildren.plateWidget.H)) * int32(i)) +
				int32(p.programSettings.StyleSheet.guiInfoTextWidgetStyle.innerPadding.paddingPixels[paddingTop])
			childData.mandatoryChildren.textWidget.X = childData.mandatoryChildren.plateWidget.X +
				int32(p.programSettings.StyleSheet.guiPlateWidgetStyle.innerPadding.paddingPixels[paddingLeft])
			childData.mandatoryChildren.textWidget.Y = childData.mandatoryChildren.plateWidget.Y +
				int32(p.programSettings.StyleSheet.guiPlateWidgetStyle.innerPadding.paddingPixels[paddingTop])
		}

		/* freshGUIInfoTextListWidget.optionalChildren = []*Widget{}
		for i, x := range p.programInfoCache.infoStrings {
			var newGUIInfoTextWidget = &Widget{
				kind: guiInfoTextListWidgetKind,
			}
			if !newGUIInfoTextWidget.validate() {
				panic("failed assert: newGUIInfoTextWidget in GUIBaseWidget.update() failed to validate!")
			}
			w.optionalChildren = append(w.optionalChildren, newGUIInfoTextWidget)
		} */
	}

	freshGUIInfoTextListWidget.render = func(w *Widget, p *ProgramContext) {
		// we need a z buffer in the next iteration

	}
	return freshGUIInfoTextListWidget
}

func getFreshInitScene(rootWidget *Widget) *Scene {
	var initScene = &Scene{}

	initScene.data = &initSceneData{
		widgets: []*Widget{rootWidget},
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

	// FIX: we set update() and then set it to something else
	initScene.update = func(s *Scene, p *ProgramContext) {
		var data, ok = s.data.(*initSceneData)
		if !ok {
			panic("failed assert: tried to update, but initScene data type assertion failed!")
		}
		for _, w := range data.widgets {
			w.update(w, p)
		}
	}

	initScene.update = func(s *Scene, p *ProgramContext) {
		if !s.dataIsReady(s, p) {
			panic("failed assert: data not ready when initScene.update() called")
		}
		for _, w := range s.data.(*initSceneData).widgets {
			w.update(w, p)
		}
	}

	initScene.render = func(s *Scene, p *ProgramContext) {
		if !s.dataIsReady(s, p) {
			panic("failed assert: data not ready when initScene.render() called")
		}
		for _, w := range s.data.(*initSceneData).widgets {
			w.render(w, p)
		}
		p.renderer.Present()
	}
	initScene.destroy = func(s *Scene) {
		// let the GC handle it but make the program panic if it's used again (could be wasteful)
		// there's still a chance the data can be accessed after the fact...
		s.handleIntent = func(w *Scene, p *ProgramContext, i Intent) {
			panic("failed assert: scene method called after destroy called (handleIntent)")
		}
		s.determineIntent = func(w *Scene, p *ProgramContext) Intent {
			panic("failed assert: scene method called after destroy called (determineIntent)")
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
	initScene.determineIntent = func(s *Scene, p *ProgramContext) Intent {
		if len(p.inputHistory.inputs) == 0 {
			panic(
				"failed assert: we should not have zero input events when trying to determine event! " +
					"(init input should be present)")
		}
		// HACK: what's the point of having it determine the Intent is IntentStart instead of just
		// skipping determineIntent and having it handle a manually created IntentStart on the first pass?
		// should the length of the input history only ever be 1 if IntentStart is sent?
		if len(p.inputHistory.inputs) == 1 {
			return Intent{IntentStart, IntentParametersStart{}}
		}
		// for now, don't account for multiple button presses in rapid succession just yet
		// NOTE: should this function be changed to get all presses of a certain type, with a parameter?
		var lastKnownMousePos = p.inputHistory.top().mouseXY
		if lastKnownMousePos != p.programInfoCache.lastKnownHoverCoord {
			p.programInfoCache.lastKnownHoverCoord = lastKnownMousePos
			return Intent{IntentMoveMouse, IntentParametersMoveMouse{
				targetScreenCoord: lastKnownMousePos,
			}}
		}

		var unhandledButtonPresses []*inputStateType = getUnhandledButtonPresses(p.inputHistory)
		if len(unhandledButtonPresses) != 0 {
			var lastUnhandledButtonPress = unhandledButtonPresses[len(unhandledButtonPresses)-1]
			if lastUnhandledButtonPress.keys[keyZoom].pressed {
				return Intent{IntentZoom, IntentParametersZoom{defaultZoomFactor}}
			}
			if lastUnhandledButtonPress.keys[keyResetView].pressed {
				return Intent{IntentResetView, IntentParametersResetView{}}
			}
			if lastUnhandledButtonPress.keys[keyMenu].pressed {
				return Intent{IntentToggleGUIInfoTextList, IntentParametersToggleGUIInfoTextList{}}
			}
		}
		// same here. assume the user does not mean for the mouse click to be handled after previous ones are
		var unhandledMouseClicks = getUnhandledMouseClicks(p.inputHistory)
		if len(unhandledMouseClicks) == 0 {
			return Intent{IntentUnknown, IntentParametersUnknown{}}
		}
		var lastUnhandledMouseClick = unhandledMouseClicks[len(unhandledMouseClicks)-1]
		// HACK?: this conversion is from uint32 to int32 which is lossy
		var newCenter = convertPlotScreenCoordToPlotPlane(lastUnhandledMouseClick.mouseXY, p)
		return Intent{IntentChangeCenter, IntentParametersChangeCenter{
			newCenter: newCenter,
		}}
	}
	initScene.handleIntent = func(s *Scene, p *ProgramContext, i Intent) {
		var data, ok = s.data.(*initSceneData)
		if !ok {
			panic("failed assert: trying to handle intent, but data is not of type initSceneData!")
		}
		// HACK: explicit enumeration of intentTypes to be handled by optionalChildren.
		// reminder: some of these need to be handled by the scene and not a widget, but they need to cause
		// an update to be propagated down
		switch i.intentType {
		case IntentStart:
			p.regenInfoText()
			for _, w := range data.widgets {
				w.handleIntent(w, p, i)
			}
		case IntentChangeCenter:
			params, ok := i.intentParameters.(IntentParametersChangeCenter)
			if !ok {
				panic("failed assert: handling intent of apparent type IntentChangeCenter, but " +
					"type assertion failed!")
			}
			// HACK: in doing this we don't verify the change propagated as a defensive measure before
			// fixing next commit
			// if p.programSettings.View.W != p.programSettings.UnitView.W *
			// XXX: should this be handled by the widget or by the scene? i suspect the scene needs to handle it,
			// then make the plotWidget update
			// XXX: if I add lastScale and lastCenter to the programSettings,
			// it denormalizes (?) the data held therein. but if I don't, then
			// I can only reliably approximate it but not get an exact reconstruction because of fp math
			// NOTE: this only works if the W/H ratio wrt the view of the plane stays consistent
			var approxOldScale = p.programSettings.View.W / p.programSettings.UnitView.W
			changePlotView(p, approxOldScale, params.newCenter)
			p.regenInfoText()
			for _, w := range data.widgets {
				w.update(w, p)
			}
		case IntentZoom:
			params, ok := i.intentParameters.(IntentParametersZoom)
			if !ok {
				panic("failed assert: handling intent of apparent type IntentZoom, but " +
					"type assertion failed!")
			}
			// NOTE: this would be much cleaner and probably more reliable if I didn't have to
			// approximate the parameters based on the existing view
			var approxOldCenter complex64 = complex(
				p.programSettings.View.X+(p.programSettings.View.W/2),
				p.programSettings.View.Y+(p.programSettings.View.H/2))
			var approxOldScale float32 = p.programSettings.View.W / p.programSettings.UnitView.W
			// HACK: lossy conversion from float64 to float32
			changePlotView(p, float32(params.factor)*approxOldScale, approxOldCenter)
			p.regenInfoText()
			for _, w := range data.widgets {
				w.update(w, p)
			}
		// FIX: is this redundant?
		case IntentResetView:
			_, ok := i.intentParameters.(IntentParametersResetView)
			if !ok {
				panic("failed assert: handling intent of apparent type IntentResetView, but type assertion failed!")
			}
			// known scale and center; it's derived from the unitview
			var approxUnitCenter = complex(
				p.programSettings.UnitView.X+(p.programSettings.UnitView.W/2),
				p.programSettings.UnitView.Y+(p.programSettings.UnitView.H/2))
			var unitScale float32 = 1.0
			changePlotView(p, unitScale, approxUnitCenter)
			p.regenInfoText()
			for _, w := range data.widgets {
				w.update(w, p)
			}
		case IntentMoveMouse:
			params, ok := i.intentParameters.(IntentParametersMoveMouse)
			if !ok {
				panic("failed assert: type assertion on params of supposed type IntentParametersMoveMouse failed!")
			}
			p.programInfoCache.lastKnownHoverCoord = params.targetScreenCoord
			p.regenInfoText()
			for _, w := range data.widgets {
				// CHECK: is this an okay architectural decision, to not update everything here?
				if w.kind == rootWidgetKind {
					w.handleIntent(w, p, i)
				}
			}
		case IntentToggleGUIInfoTextList:
			_, ok := i.intentParameters.(IntentParametersToggleGUIInfoTextList)
			if !ok {
				panic("failed assert: type assertion of params of supposed type IntentToggleGUIInfoTextList failed!")
			}
			data, ok = s.data.(*initSceneData)
			if !ok {
				panic("failed assert: type assertion of initSceneData in initScene.handleIntent failed!")
			}
			// find the GUIInfoTextListWidget and make it invisible or remove it (probably remove it)
			var initSceneRootWidget *Widget
			for _, w := range data.widgets {
				if w.kind == rootWidgetKind {
					initSceneRootWidget = w
				}
			}
			if initSceneRootWidget == nil {
				panic("failed assert: initScene has no root widget!")
			}

			var initSceneGUIBaseWidget *Widget
			for _, c := range initSceneRootWidget.optionalChildren {
				if c.kind == guiBaseWidgetKind {
					initSceneGUIBaseWidget = c
				}
			}
			if initSceneGUIBaseWidget == nil {
				panic("failed assert: initScene has no GUIBaseWidget!")
			}
			var initSceneGUIInfoTextListWidget *Widget
			for i, c := range initSceneGUIBaseWidget.optionalChildren {
				if c.kind == guiInfoTextListWidgetKind {
					initSceneGUIInfoTextListWidget = c
					initSceneGUIBaseWidget.optionalChildren = append(initSceneGUIBaseWidget.optionalChildren[:i], initSceneGUIBaseWidget.optionalChildren[i+1:]...)
				}
			}
			if initSceneGUIInfoTextListWidget == nil {
				getFreshGUIInfoTextListWIdget(initSceneGUIBaseWidget)
			}
			for _, w := range data.widgets {
				w.update(w, p)
			}
		default:

		}
	}
	return initScene
}

type GUIPlateWidgetData struct {
	texture *sdl.Texture
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

	var rootWidget = *getFreshRootWidget()
	var plotWidget = *getFreshPlotWidget(&rootWidget)
	rootWidget.registerOptionalChildWidget(&plotWidget)

	var guiBaseWidget = *getFreshGUIBaseWidget(&rootWidget)
	rootWidget.registerOptionalChildWidget(&guiBaseWidget)

	// var guiInfoTextListWidget = *getFreshGUIInfoTextListWidget()

	// difference from what I originally intended: this widget is the same as the screen size
	var guiInfoTextListWidget = Widget{
		X: 0,
		Y: 0,
		W: guiBaseWidget.W,
		H: guiBaseWidget.H,

		kind: guiInfoTextListWidgetKind,

		parent:           &guiBaseWidget,
		optionalChildren: []*Widget{},

		data: GUIInfoTextListWidgetData{},

		initData:     nil,
		update:       nil,
		render:       nil,
		handleIntent: nil,
		dataIsReady:  nil,
		dataIsSane:   nil,
	}

	// this seems to have no purpose at the moment given I can initialize the data in the getFresh[...]() function.
	// can this be repurposed to be a "reInitData" or something else helpful?
	guiInfoTextListWidget.initData = func(w *Widget, p *ProgramContext) {
		w.data = GUIInfoTextListWidgetData{}
	}

	guiInfoTextListWidget.dataIsSane = func(w *Widget, p *ProgramContext) bool {
		// what is this supposed to be doing again?
		return w.dataIsReady(w, p)
	}

	guiInfoTextListWidget.dataIsReady = func(w *Widget, p *ProgramContext) bool {
		data, ok := w.data.(*GUIInfoTextListWidgetData)
		// XXX: this never returns false. i think I'm deviating from my own plan here
		if !ok {
			panic("failed assert: type assertion failed on data of guiInfoTextListWidget, of supposed type GUIInfoTextListWidgetData!")
		}
		if !data.validate() {
			panic("failed assert: data of widget performing initData (guiInfoTextListWidget) failed to validate!")
		}
		return true
	}

	guiInfoTextListWidget.update = func(w *Widget, p *ProgramContext) {
		// every time we update, we need to update the textures stored in data
		// => when we add said textures to the data, the validator function/dataIsReady/dataIsSane needs to
		//    be updated

		// regen textures from data (should that be a function that every widget has, if it has associated textures?
		// i'm gonna go with that's included in the scope of update() and then we can just call render() for the
		// rendering
		if w.kind != guiInfoTextListWidgetKind {
			panic("failed assert: kind of guiInfoTextListWidget is not guiInfoTextListWidgetKind!")
		}
		/* var data, ok = w.data.(*GUIInfoTextListWidgetData)
		if !ok {
			panic("failed assert: type assertion failed in guiInfoTextListWidget.update(), " +
				"with supposed type *GUIInfoTextListWidgetData!")
		} */
		// regenerate the children based on the available infoStrings
		w.optionalChildren = []*Widget{}
		for i, iS := range p.programInfoCache.infoStrings {
			var freshInfoTextWidgetFromIS = &Widget{
				// leaving dimensions undefined
				Y: func() int32 {
					var padding int32
					if i != 0 {
						padding = int32(p.programSettings.StyleSheet.guiInfoTextListWidgetStyle.paddingBetweenEntries * (i - 1))
					}
					return int32((p.programSettings.StyleSheet.guiTextWidgetStyle.font.Height() * i)) + padding
				}(),

				kind:   guiInfoTextWidgetKind,
				parent: w,

				// must add the children afterwards
				data: &GUIInfoTextWidgetData{
					correspondingInfoString: iS.infoStringKind,
					mandatoryChildren: GUIInfoTextWidgetMandatoryChildren{
						textWidget:  nil,
						plateWidget: nil,
					},
				},

				optionalChildren: []*Widget{},

				initData:     nil,
				update:       nil,
				render:       nil,
				handleIntent: nil,
				dataIsReady:  nil,
				dataIsSane:   nil,
			}

			freshInfoTextWidgetFromIS.initData = func(w *Widget, p *ProgramContext) {
				var data, ok = w.data.(*GUIInfoTextWidgetData)
				if !ok {
					panic("failed assert: tried to init data in freshInfoTextWidgetFromIS, " +
						"but type assertion of data failed!")
				}
				data.mandatoryChildren.plateWidget = getFreshPlateWidget(w)
				data.mandatoryChildren.textWidget = getFreshTextWidget(w)
			}
			freshInfoTextWidgetFromIS.update = func(w *Widget, p *ProgramContext) {
				var data, ok = w.data.(*GUIInfoTextWidgetData)
				if !ok {
					panic("failed assert: tried to update data in freshInfoTextWidgetFromIS, " +
						"but type assertion of data failed!")
				}
				textWidgetData, ok := data.mandatoryChildren.textWidget.data.(*GUITextWidgetData)
				if !ok {
					panic("failed assert: type assert failed on freshInfoText[...] textWidget child data!")
				}
				// NOTE: there's probably some useless redundancy here that can be fixed in the next iteration
				textWidgetData.text = p.programInfoCache.infoStrings[InfoStringKind(i)].string
				if len(textWidgetData.text) == 0 {
					return
				}
				textSurface, err := p.programSettings.StyleSheet.
					guiTextWidgetStyle.font.RenderUTF8Solid(textWidgetData.text,
					*p.programSettings.StyleSheet.guiTextWidgetStyle.textColor)
				if err != nil {
					panic("failed to generate textSurface: " + err.Error())
				}
				textWidgetData.texture, err = p.renderer.CreateTextureFromSurface(textSurface)
				if err != nil {
					panic("failed to generate textTexture: " + err.Error())
				}
				_, _, textWidgetTextureW, textWidgetTextureH, err := textWidgetData.texture.Query()
				if err != nil {
					panic("failed to query textTexture: " + err.Error())
				}
				// then update the plateWidget based on the textWidget
				plateWidgetData, ok := data.mandatoryChildren.plateWidget.data.(*GUIPlateWidgetData)
				if !ok {
					panic("failed assert: type assert failed for supposed GUIPlateWidgetData in freshInfoText[...].update()")
				}
				plateWidgetData.texture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET,
					int32(p.programSettings.StyleSheet.guiPlateWidgetStyle.innerPadding.paddingPixels[paddingLeft]+
						p.programSettings.StyleSheet.guiPlateWidgetStyle.innerPadding.paddingPixels[paddingRight])+
						textWidgetTextureW,
					int32(p.programSettings.StyleSheet.guiPlateWidgetStyle.innerPadding.paddingPixels[paddingTop]+
						p.programSettings.StyleSheet.guiPlateWidgetStyle.innerPadding.paddingPixels[paddingBottom])+
						textWidgetTextureH,
				)
				if err != nil {
					panic("failed to create texture: " + err.Error())
				}
				err = p.renderer.SetRenderTarget(plateWidgetData.texture)
				if err != nil {
					panic("failed to set render target to plateWidgetData.texture: " + err.Error())
				}
				defer func() {
					err = p.renderer.SetRenderTarget(nil)
					if err != nil {
						panic("failed to reset render target: " + err.Error())
					}
				}()
				previousDrawColorR, previousDrawColorG, previousDrawColorB, previousDrawColorA, err := renderer.GetDrawColor()
				if err != nil {
					panic("failed to set draw color: " + err.Error())
				}
				defer func() {
					err := renderer.SetDrawColor(previousDrawColorR, previousDrawColorG, previousDrawColorB, previousDrawColorA)
					if err != nil {
						panic("failed to reset draw color: " + err.Error())
					}
				}()
				err = p.renderer.Clear()
				if err != nil {
					panic("failed to clear plateWidgetData.texture: " + err.Error())
				}
			}

			// XXX: i think there's useless redundancy here right now because I [...]
			freshInfoTextWidgetFromIS.render = func(w *Widget, p *ProgramContext) {
				var data, ok = w.data.(*GUIInfoTextWidgetData)
				if !ok {
					panic("failed assert: type assert failed in freshInfoText[...].render on GUIInfoTextWidget data!")
				}
				plateWidgetData, ok := data.mandatoryChildren.plateWidget.data.(*GUIPlateWidgetData)
				if !ok {
					panic("failed assert: type assert failed in freshInfoText[...].render on GUIPlateWidget data!")
				}
				textWidgetData, ok := data.mandatoryChildren.textWidget.data.(*GUITextWidgetData)
				if !ok {
					panic("failed assert: type assert failed in freshInfoText[...].render on GUIPlateWidget data!")
				}

				// HACK: this is a bad place to put this and our data should be ready beforehand.
				// this is just to get a working GUI so I can go to the next iteration.
				if plateWidgetData.texture == nil {
					plateWidgetData.texture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, 1, 1)
					if err != nil {
						panic(err)
						// XXX: completed this part without remembering the context
					}
				}
				if textWidgetData.texture == nil {
					textWidgetData.texture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, 1, 1)
					if err != nil {
						panic(err)
						// XXX: completed this part without remembering the context
					}
				}

				_, _, plateWidgetTextureW, plateWidgetTextureH, err := plateWidgetData.texture.Query()
				if err != nil {
					panic("failed to query plateWidgetTexture: " + err.Error())
				}
				_, _, textWidgetTextureW, textWidgetTextureH, err := textWidgetData.texture.Query()
				if err != nil {
					panic("failed to query textWidgetTexture: " + err.Error())
				}
				var textAbsolutePosition = getAbsolutePosition(data.mandatoryChildren.textWidget)
				var plateAbsolutePosition = getAbsolutePosition(data.mandatoryChildren.plateWidget)

				err = p.renderer.Copy(plateWidgetData.texture, &sdl.Rect{0, 0, plateWidgetTextureW, plateWidgetTextureH},
					&sdl.Rect{plateAbsolutePosition[0], plateAbsolutePosition[1], plateWidgetTextureW, plateWidgetTextureH})
				if err != nil {
					panic("failed to copy plateWidget texture: " + err.Error())
				}

				err = p.renderer.Copy(textWidgetData.texture, &sdl.Rect{0, 0, textWidgetTextureW, textWidgetTextureH},
					&sdl.Rect{textAbsolutePosition[0], textAbsolutePosition[1], textWidgetTextureW, textWidgetTextureH})
				if err != nil {
					panic("failed to copy textWidget texture: " + err.Error())
				}
				// does not handle present() - only the scene does
			}

			freshInfoTextWidgetFromIS.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {}
			freshInfoTextWidgetFromIS.dataIsReady = func(w *Widget, p *ProgramContext) bool {
				for _, _ = range w.optionalChildren {
					panic("failed assert: GUIInfoTextWidget has optional children, when it shouldn't!")
				}
				var data, ok = w.data.(*GUIInfoTextWidgetData)
				if !ok {
					panic("failed assert: type assertion failed in freshInfoText[...].dataIsReady on " +
						"GUIInfoTextWidget data!")
				}
				if !data.mandatoryChildren.plateWidget.dataIsReady(data.mandatoryChildren.plateWidget, p) ||
					data.mandatoryChildren.textWidget.dataIsReady(data.mandatoryChildren.textWidget, p) {
					return false
				}
				return true
			}
			freshInfoTextWidgetFromIS.initData(freshInfoTextWidgetFromIS, p)
			if !freshInfoTextWidgetFromIS.validate() {
				panic("failed assert: failed to validate freshInfoTextWidget in " +
					"guiInfoTextListWidget.update()!")
			}

			w.optionalChildren = append(w.optionalChildren, freshInfoTextWidgetFromIS)
		}

		// let those children update themselves
		for _, c := range w.optionalChildren {
			c.update(c, p)
		}
	}

	guiInfoTextListWidget.render = func(w *Widget, p *ProgramContext) {
		for _, c := range w.optionalChildren {
			c.render(c, p)
		}
	}

	guiInfoTextListWidget.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {
		// given you can possible toggle different parts of the UI, those need to stay in state somewhere in the
		// program context
		// in fact, why are the widgets handling intent in the first place again?

		/* if i.intentType == IntentToggleGUIInfoTextList {
			w.data.hidden = !w.data.hidden
			for _, c := range w.optionalChildren {
				c.handleIntent(w, p, i)
			}
		} */
	}

	ok := guiInfoTextListWidget.validate()
	if !ok {
		panic("failed assert: guiInfoTextListWidget failed to validate!")
	}
	guiBaseWidget.registerOptionalChildWidget(&guiInfoTextListWidget)

	var programContext = ProgramContext{
		window:          window,
		renderer:        renderer,
		programSettings: (&ProgramSettings{}).initialize(),
	}

	rootWidget.initData(&rootWidget, &programContext)

	var sceneStack = SceneStack{
		scenes: []*Scene{},
	}

	// REVIEW: is this fine?
	programContext.sceneStack = &sceneStack

	programContext.inputHistory = (&InputHistoryType{}).initialize()
	programContext.inputHistory.pushFront((&inputStateType{}).initialize())

	// programContext.programSettings.StyleSheet = (&StyleSheet{}).initialize()
	err = ttf.Init()
	if err != nil {
		panic("failed to init ttf: " + err.Error())
	}
	font, err := ttf.OpenFont(programContext.programSettings.DefaultFontLocation, programContext.programSettings.DefaultFontSize)
	if err != nil {
		panic("failed to open font: " + err.Error())
	}
	programContext.programSettings.StyleSheet.guiTextWidgetStyle.font = font
	programContext.programInfoCache = &ProgramInfoCache{}

	if !rootWidget.validate() {
		panic("failed assert: rootWidget failed to validate")
	}
	if !programContext.validate() {
		panic("failed assert: program context did not validate")
	}

	// sanity check
	ok = checkIfParentsAndChildrenKnowEachOther([]*Widget{
		&rootWidget,
		&plotWidget,
		&guiBaseWidget,
		&guiInfoTextListWidget,
	})
	if !ok {
		// TODO: phrasing?
		panic("failed assert: for some parent and child Widget pair during checkIfParentsAndChildrenKnowEachOther()" +
			", mutual knowledge is asymmetric!")
	}

	// set up initScene

	var initScene = *getFreshInitScene(&rootWidget)

	if !initScene.validate() {
		panic("failed assert: failed to validate initScene")
	}

	// load settings
	loadedProgramSettings, err := loadProgramSettings()
	if err == nil {
		programContext.programSettings = loadedProgramSettings
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

	sceneStack.Replace(&initScene, &programContext)

	// ok because we know what the top scene is
	// var intent = initScene.determineIntent(&initScene, &programContext) // no reason to do this
	initScene.handleIntent(&initScene, &programContext, Intent{IntentStart, IntentParametersStart{}})
	initScene.render(&initScene, &programContext)

	// profiling
	f, err := os.Create("cpu.prof")
	if err != nil {
		log.Fatal("could not create CPU profile: ", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		log.Fatal("could not start CPU profile: ", err)
	}

	// main event loop
	for e := sdl.PollEvent(); true; e = sdl.PollEvent() {
		if e == nil {
			continue
		}

		// QUESTIONABLE: should we have an exit input, which then translates into an exit Intent which is then acted on?
		// i think a close intent is helpful maybe, that closes the active GUI widget/element/state broadly?
		switch e.(type) {
		case *sdl.QuitEvent:
			pprof.StopCPUProfile()
			sdl.Quit()
			os.Exit(0)
		}
		if !sdlEventTypeIsInputType(e.GetType()) {
			continue
		}
		var newInputState = (&inputStateType{}).initialize()
		registerInputFromSDLEvent(&programContext, newInputState, e)
		if programContext.inputHistory.top() == newInputState {
			continue
		}
		// guaranteed the new input is unique
		programContext.inputHistory = programContext.inputHistory.pushFront(newInputState)
		var topScene = sceneStack.Top()
		// topScene.handleEvent(topScene, &programContext, &inputState)
		var intent Intent = topScene.determineIntent(topScene, &programContext)
		topScene.handleIntent(topScene, &programContext, intent)
		topScene.render(topScene, &programContext)
	}
}

func (i *InputHistoryType) top() *inputStateType {
	return i.inputs[len(i.inputs)-1]
}
func (iH *InputHistoryType) pushFront(iS *inputStateType) *InputHistoryType {
	iH.inputs = append(iH.inputs, iS)
	return iH
}

func sdlEventTypeIsInputType(eventType uint32) bool {
	// HACK: for now. i may want to change this later. this would guarantee that we only update the inputs when an input actually occurs
	var inputTypes = [...]uint32{
		sdl.MOUSEBUTTONDOWN,
		sdl.MOUSEBUTTONUP,
		sdl.KEYDOWN,
		sdl.KEYUP,
		sdl.MOUSEMOTION,
	}
	// HACK?: generating this every time the function is called. shouldn't affect performance yet
	for _, x := range inputTypes {
		if eventType == x {
			return true
		}
	}
	return false
}

// is using the same values that SDL uses a problem?
// we get an sdl.Event, we registerInputFromSDLEvent() which changes the inputState, then (in the main loop),
// the top scene handles the event

// maybe determineIntent() instead, then handleIntent()?
// MAJOR ASSUMPTION: the events are always processed and provided by SDL (!!) in the order they occur
func registerInputFromSDLEvent(p *ProgramContext, inputState *inputStateType, e sdl.Event) {
	// do basic setup
	var lastInputState = p.inputHistory.top()
	var nextInputStateIfUntouched = (&inputStateType{}).initialize()

	switch e.(type) {
	// my assumption is that each case will proceed until it finds a break
	case *sdl.KeyboardEvent:
	case *sdl.MouseButtonEvent:
	case *sdl.MouseMotionEvent:

		// all fields assumed to have the same sizes
		for b, _ := range lastInputState.mouseButtons {
			// XXX: check this
			if lastInputState.mouseButtons[b].held && !lastInputState.mouseButtons[b].released {
				(*nextInputStateIfUntouched).mouseButtons[b].held = true
			} else {
				(*nextInputStateIfUntouched).mouseButtons[b].held = false
			}
		}
		for b, _ := range lastInputState.keys {
			if lastInputState.keys[b].held && !lastInputState.keys[b].released {
				(*nextInputStateIfUntouched).keys[b].held = true
			} else {
				(*nextInputStateIfUntouched).keys[b].held = false
			}
		}
		break

	default:
		panic("failed assert: tried to register input from sdl event but" +
			"input type was not accounted for by switch case!")
	}

	switch e.(type) {
	case *sdl.KeyboardEvent:
		// making a copy of the input state
		var nextInputState = &*nextInputStateIfUntouched

		// whichever button's state is updating: if it's pressed or released
		var ev = e.(*sdl.KeyboardEvent)
		var pressed bool
		var released bool
		if ev.State == sdl.PRESSED && ev.Repeat == 0 {
			pressed = true
		}
		if ev.State == sdl.RELEASED && ev.Repeat == 0 {
			released = false
		}
		// do nothing on repeat
		switch ev.Keysym.Sym {
		case sdl.K_m:
			nextInputState.keys[keyMenu].pressed = pressed
			nextInputState.keys[keyMenu].released = released
		case sdl.K_z:
			nextInputState.keys[keyZoom].pressed = pressed
			nextInputState.keys[keyZoom].released = released
		case sdl.K_r:
			nextInputState.keys[keyResetView].pressed = pressed
			nextInputState.keys[keyResetView].released = released
		default:
			log.Default().Println("NOTE: key pressed that will not be registered: sym " + string(ev.Keysym.Sym))
		}
		// HACK: doesn't account for overflow after ~49 days of continuous use
		nextInputState.timestamp = uint64(ev.Timestamp)
		*inputState = *nextInputState
	case *sdl.MouseMotionEvent:
		var ev = e.(*sdl.MouseMotionEvent)

		var nextInputState = &*nextInputStateIfUntouched
		nextInputState.mouseXY[0], nextInputState.mouseXY[1] = uint32(ev.X), uint32(ev.Y)
		nextInputState.timestamp = uint64(ev.Timestamp)
		*inputState = *nextInputState

	case *sdl.MouseButtonEvent:
		// we get a mouse button event and assume this is the only change.
		// we then create a new input event based on the last one
		// and change what needs to be changed based on the new input and the last state

		var ev = e.(*sdl.MouseButtonEvent)

		/* var newPressedState = false
		if ev.State == sdl.PRESSED {
			newPressedState = true
		} */
		var buttonChanged int = -1
		switch ev.Button {
		case sdl.BUTTON_LEFT:
			buttonChanged = mouseButtonLeft
		case sdl.BUTTON_RIGHT:
			buttonChanged = mouseButtonRight
		case sdl.BUTTON_MIDDLE:
			buttonChanged = mouseButtonMiddle
		default:
			panic("failed assert: sdl event was MouseButtonEvent, but neither left/right/middle!")
		}

		var pressed bool
		var heldPossible bool
		var released bool
		// XXX: this logic probably includes a bug somewhere. i would need to try it on paper
		if ev.State == sdl.PRESSED && !nextInputStateIfUntouched.mouseButtons[buttonChanged].held {
			pressed = true
			heldPossible = false
		}
		if ev.State == sdl.RELEASED && nextInputStateIfUntouched.mouseButtons[buttonChanged].held {
			released = true
			heldPossible = false
		}

		var nextInputState = &*nextInputStateIfUntouched
		if heldPossible == false {
			nextInputState.mouseButtons[buttonChanged].held = false
		}
		nextInputState.mouseButtons[buttonChanged].pressed = pressed
		nextInputState.mouseButtons[buttonChanged].released = released

		// get mouse XY just for good measure
		nextInputState.mouseXY[0] = uint32(ev.X)
		nextInputState.mouseXY[1] = uint32(ev.Y)

		nextInputState.timestamp = uint64(ev.Timestamp)
		*inputState = *nextInputState
	case *sdl.QuitEvent:
		os.Exit(0)
	}
}

func regenPlotValues(p *ProgramContext) *[]int16 {
	// this might be expensive...
	var out = make([]int16, p.programSettings.PlotResX*p.programSettings.PlotResY)
	var wg sync.WaitGroup
	var chunks int32 = 1
	var chunkSize int32 = p.programSettings.PlotResX * p.programSettings.PlotResY
	if p.programSettings.PlotResX%8 == 0 {
		chunks = 8
		chunkSize = (p.programSettings.PlotResX * p.programSettings.PlotResY) / 8
	}
	for i := int32(0); i < chunks; i++ {
		wg.Add(1)
		// with 8 goroutines, this takes about 1.5s on my laptop
		go func() {
			defer wg.Done()
			start := int32(i) * chunkSize
			end := (int32(i) + 1) * chunkSize
			for i = start; i < end; i++ {
				if i > math.MaxInt32 {
					// off by one maybe but it's inconsequential right now
					panic("failed assert: more than MaxInt32 + 1 values in plot!")
				}
				// y coord
				// HACK: lossy type cast from int32 to uint32 (there's no reason it should matter in a RL scenario)
				var quotient = uint32(i) / uint32(p.programSettings.PlotResX)
				var remainder = uint32(i) % uint32(p.programSettings.PlotResX)
				var currentPlotScreenCoord = [2]uint32{remainder, quotient}
				var currentPlotPlaneCoord = convertPlotScreenCoordToPlotPlane(currentPlotScreenCoord, p)
				var currentPlotValue = calculatePlotValueFromPlaneCoord(currentPlotPlaneCoord, p)
				out[i] = currentPlotValue
			}
		}()
	}
	wg.Wait()
	return &out
}

func (p *ProgramContext) regenInfoText() {
	p.programInfoCache.infoStrings = [numInfoStringKinds]InfoString{}
	var hoverCoordString = "current hover coord: (" +
		strconv.Itoa(int(p.programInfoCache.lastKnownHoverCoord[0])) +
		", " + strconv.Itoa(int(p.programInfoCache.lastKnownHoverCoord[1])) + ")"
	p.programInfoCache.infoStrings[infoStringKindHoverScreenCoord] = InfoString{
		infoStringKindHoverScreenCoord,
		hoverCoordString,
	}
	p.programInfoCache.infoStrings[infoStringKindHoverPlaneCoord] = InfoString{
		infoStringKindHoverPlaneCoord,
		"current hover coord in complex plane: " +
			fmt.Sprintf("(%.6f, %.6f)", real(convertPlotScreenCoordToPlotPlane(p.programInfoCache.lastKnownHoverCoord, p)),
				imag(convertPlotScreenCoordToPlotPlane(p.programInfoCache.lastKnownHoverCoord, p))),
	}
}

// plotScreen coord is in the range 0-PlotResX, 0-PlotResY
// plotPlane coord is in the range PlotRangeRealLower-PlotRangeRealUpper, 0-PlotRangeImagLower, PlotRangeImageUpper
func convertPlotScreenCoordToPlotPlane(c [2]uint32, p *ProgramContext) complex64 {
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

// HACK: (?) scale and center are float32 here. can we squeeze some more resolution out of this?
func changePlotView(p *ProgramContext, scale float32, center complex64) {
	var newView = getViewFromScaleAndCenter(p, scale, center)
	p.programSettings.View = newView
	p.sceneStack.Top().update(p.sceneStack.Top(), p)
}

func getViewFromScaleAndCenter(p *ProgramContext, scale float32, center complex64) *sdl.FRect {
	return &sdl.FRect{
		X: real(center) - (p.programSettings.UnitView.W*scale)/2,
		Y: imag(center) - (p.programSettings.UnitView.H*scale)/2,
		W: p.programSettings.UnitView.W * scale,
		H: p.programSettings.UnitView.H * scale,
	}
}

func getUnhandledMouseClicks(in *InputHistoryType) []*inputStateType {
	// NOTE: when inputStates containing a mouse press are returned (i.e. mouse clicks), those clicks
	// considered handled and no longer appear in the input history
	var unhandledMouseClicks []*inputStateType
	var inCopyWithoutMouseClicks InputHistoryType
	for _, iS := range in.inputs {
		if iS.mouseButtons[mouseButtonLeft].pressed {
			unhandledMouseClicks = append(unhandledMouseClicks, iS)
		} else {
			inCopyWithoutMouseClicks.inputs = append(inCopyWithoutMouseClicks.inputs, iS)
		}
	}
	*in = inCopyWithoutMouseClicks
	return unhandledMouseClicks
}

// so this just leaves the button holds in the event history while removing the
// button presses? is that going to cause issues?
func getUnhandledButtonPresses(in *InputHistoryType) []*inputStateType {
	var unhandledButtonPresses []*inputStateType
	var inCopyWithoutButtonPresses InputHistoryType
	for _, iS := range in.inputs {
		// will this cause issues?
		if iS.keys[keyZoom].pressed || iS.keys[keyResetView].pressed {
			unhandledButtonPresses = append(unhandledButtonPresses, iS)
			// if two pressed at the same time, keyZoom takes priority
		} else {
			inCopyWithoutButtonPresses.inputs = append(inCopyWithoutButtonPresses.inputs, iS)
		}
	}
	*in = inCopyWithoutButtonPresses
	return unhandledButtonPresses
}

func checkIfParentsAndChildrenKnowEachOther(widgets []*Widget) bool {
	for _, w := range widgets {
		for _, child := range w.optionalChildren {
			if child.parent != w {
				return false
			}
		}
	}
	return true
}

// NOTE: regenerating the functions every time is probably very inefficient.
// i might consider instantiating them from a prototype next iteration
func getFreshPlateWidget(parent *Widget) *Widget {
	var freshPlateWidget = &Widget{
		// come back to this
		kind:             guiPlateWidgetKind,
		parent:           parent,
		optionalChildren: []*Widget{},
		data:             &GUIPlateWidgetData{},

		initData:     nil,
		update:       nil,
		render:       nil,
		handleIntent: nil,
		dataIsReady:  nil,
		dataIsSane:   nil,
	}
	freshPlateWidget.initData = func(w *Widget, p *ProgramContext) {
		var data, ok = w.data.(*GUIPlateWidgetData)
		if !ok {
			panic("failed assert: data not of type *GUIPlateWidgetData " +
				"in type assert in freshPlateWidget.initData()!")
		}
		var err error
		data.texture, err = p.renderer.CreateTexture(sdl.PIXELFORMAT_RGBA8888, sdl.TEXTUREACCESS_TARGET, 1, 1)
		if err != nil {
			panic("failed to create texture in freshPlateWidget.initData(): " + err.Error())
		}
	}
	freshPlateWidget.update = func(w *Widget, p *ProgramContext) {
		panic("failed assert: freshPlateWidget.update() called, but freshPlateWidget does not update itself! " +
			"(why did the parent propagate update downwards?)")
	}
	freshPlateWidget.render = func(w *Widget, p *ProgramContext) {
		var absolutePosition = getAbsolutePosition(w)
		var data, ok = w.data.(*GUIPlateWidgetData)
		if !ok {
			panic("failed assert: freshPlateWidget.render tried to type assert data, but was not of apparent type *GUIPlateWidgetData!")
		}
		_, _, W, H, err := data.texture.Query()
		err = p.renderer.Copy(data.texture, &sdl.Rect{0, 0, W, H}, &sdl.Rect{absolutePosition[0], absolutePosition[1], W, H})
		if err != nil {
			panic("failed to copy freshPlateWidget texture: " + err.Error())
		}
	}
	freshPlateWidget.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {

	}
	freshPlateWidget.dataIsReady = func(w *Widget, p *ProgramContext) bool {
		data, ok := w.data.(*GUIPlateWidgetData)
		if !ok {
			panic("failed assert: data not of expected type during type assert in freshPlateWidget.dataIsReady()!")
		}
		_, _, W, H, err := data.texture.Query()
		if err != nil {
			panic("failed to query texture: " + err.Error())
		}
		if W == 1 && H == 1 {
			return false
		}
		return true
	}
	freshPlateWidget.dataIsSane = func(w *Widget, p *ProgramContext) bool {
		if w.data == nil {
			return false
		}
		data, ok := w.data.(*GUIPlateWidgetData)
		if !ok {
			panic("failed assert: data not of expected type (failed type assert) in freshPlateWidget.dataIsSane()!")
		}
		if data.texture == nil {
			return false
		}
		return true
	}
	if !freshPlateWidget.validate() {
		panic("failed assert: freshPlateWidget failed to validate in getFreshPlateWidget()!")
	}
	return freshPlateWidget
}
func getFreshTextWidget(parent *Widget) *Widget {
	var freshTextWidget = &Widget{
		kind:             guiTextWidgetKind,
		parent:           parent,
		optionalChildren: []*Widget{},
		data:             &GUITextWidgetData{},

		initData:     nil,
		update:       nil,
		render:       nil,
		handleIntent: nil,
		dataIsReady:  nil,
		dataIsSane:   nil,
	}
	freshTextWidget.initData = func(w *Widget, p *ProgramContext) {}
	freshTextWidget.update = func(w *Widget, p *ProgramContext) {
		// the widget shouldn't update itself in this current iteration, that's
		// handled by a parent
		panic("failed assert: freshTextWidget.update() called, but parent should handle updates!")
	}
	freshTextWidget.render = func(w *Widget, p *ProgramContext) {
		// would it be better to use an ECS for GUI stuff?
		data, ok := w.data.(*GUITextWidgetData)
		if !ok {
			panic("failed assert: type assert failed on data in freshTextWidget.render()!")
		}
		absolutePosition := getAbsolutePosition(w)
		_, _, W, H, err := data.texture.Query()
		if err != nil {
			panic("failed to query texture: " + err.Error())
		}
		err = p.renderer.Copy(data.texture, &sdl.Rect{0, 0, W, H}, &sdl.Rect{absolutePosition[0], absolutePosition[1], W, H})
		if err != nil {
			panic("failed to copy texture: " + err.Error())
		}
	}
	freshTextWidget.handleIntent = func(w *Widget, p *ProgramContext, i Intent) {

	}
	freshTextWidget.dataIsReady = func(w *Widget, p *ProgramContext) bool {
		return true
	}
	freshTextWidget.dataIsSane = func(w *Widget, p *ProgramContext) bool {
		return true
	}
	if !freshTextWidget.validate() {
		panic("failed assert: freshTextWidget failed to validate in getFreshTextWidget()!")
	}
	return freshTextWidget
}

/*
func createInfoTextWidgetFromIS(iS InfoString) *Widget {
	var itWidgetFromIS = &Widget{
		// leaving dimensions undefined

		kind: guiInfoTextWidgetKind,
		parent: w,

		data: &GUIInfoTextWidgetData{
			correspondingInfoString: iS.infoStringKind,
		},



	}
} */

func rangeInts(min int32, oneovermax int32) []int {
	var out = []int{}
	for i := range oneovermax - min {
		out = append(out, int(i)+int(min))
	}
	if len(out) != int(oneovermax-min) {
		panic("failed assert: length of rangeInts output incorrect!")
	}
	return out
}
