package main

import (
	"fmt"
	// "github.com/go-gl/gl"
	cl "github.com/go-gl/cl/v1.2/cl"
	_ "github.com/jgillich/go-opencl/cl"
	"math/rand"
	"unsafe"
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
*/

/* 	general structure is as before: we render screens tied to a rect

 */

const DataSize = 1024

var KernelSource = `
__kernel void square(
	__global float* input,
	__global float* output,
	const unsigned int count)
{
	int i = get_global_id(0)
	if (i < count)
			output[i] = input[i] * input[i]
}
` + "\x00" // null terminate the string

func main() {
	// i might have to use SDL in OpenGL mode

	// https://ulhpc-tutorials.readthedocs.io/en/latest/gpu/opencl/
	// for now, just working with the tutorial verbatim

	data := make([]float32, DataSize)
	for x := 0; x < len(data); x++ {
		data[x] = rand.Float32()*99 + 1
	}

	// get default device
	var device cl.DeviceId
	err := cl.GetDeviceIDs(nil, cl.DEVICE_TYPE_GPU, 1, &device, nil)
	if err != cl.SUCCESS {
		panic("failed to get device ids")
	}
	var errptr *cl.ErrorCode

	// create computer context
	context := cl.CreateContext(nil, 1, &device, nil, nil, errptr)
	// this is supposedly always false? and the conversion is redundant?
	if errptr != nil && *errptr != cl.SUCCESS {
		panic("failed to create context")
	}
	defer cl.ReleaseContext(context)

	// create a command queue
	cq := cl.CreateCommandQueue(context, device, 0, errptr)
	// same issue here. is this an error with the tutorial?
	if errptr != nil && *errptr != cl.SUCCESS {
		panic("failed to create command queue")
	}

	// create program
	srcptr := cl.Str(KernelSource)
	program := cl.CreateProgramWithSource(context, 1, &srcptr, nil, errptr)
	if errptr != nil && *errptr != cl.SUCCESS {
		panic("failed to create program")
	}
	defer cl.ReleaseProgram(program)

	err = cl.BuildProgram(program, 1, &device, nil, nil, nil)
	if err != cl.SUCCESS {
		var length uint64
		buffer := make([]byte, DataSize)

		cl.GetProgramBuildInfo(program, device, cl.PROGRAM_BUILD_LOG, uint64(len(buffer)), unsafe.Pointer(&buffer[0]), &length)
		panic("failed to build program: \n" + string(buffer[0:length]))
	}

	// get kernel
	kernel := cl.CreateKernel(program, cl.Str("square"+"\x00"), errptr)
	if errptr != nil && *errptr != cl.SUCCESS {
		panic("failed to create compute kernel")
	}
	defer cl.ReleaseKernel(kernel)

	// create buffers (memory for opencl program?)
	input := cl.CreateBuffer(context, cl.MEM_READ_ONLY, 4*DataSize, nil, errptr)
	if errptr != nil {
		panic("failed to create input buffer")
	}
	defer cl.ReleaseMemObject(input)
	// i'm guessing 4 refers to 32-bit values?
	output := cl.CreateBuffer(context, cl.MEM_WRITE_ONLY, 4*DataSize, nil, errptr)
	if errptr != nil {
		panic("failed to create output buffer")
	}

	// write data
	err = cl.EnqueueWriteBuffer(cq, input, cl.TRUE, 0, 4*DataSize, unsafe.Pointer(&data[0]), 0, nil, nil)
	if err != cl.SUCCESS {
		panic("failed to write data to input array")
	}

	// set kernel args
	count := uint32(DataSize)
	// i'm guessing argIndex 0 is the input buffer, and 8 refers to a 64 bit pointer length?
	err = cl.SetKernelArg(kernel, 0, 8, unsafe.Pointer(&input))
	if err != cl.SUCCESS {
		panic("failed to set kernel arg 0")
	}
	err = cl.SetKernelArg(kernel, 1, 8, unsafe.Pointer(&output))
	if err != cl.SUCCESS {
		panic("failed to set kernel arg 1")
	}
	err = cl.SetKernelArg(kernel, 2, 4, unsafe.Pointer(&count))
	if err != cl.SUCCESS {
		panic("failed to set kernel arg 2")
	}

	// okay, so we query to get the maximum local group size...
	local := uint64(0)
	err = cl.GetKernelWorkGroupInfo(kernel, device, cl.KERNEL_WORK_GROUP_SIZE, 8, unsafe.Pointer(&local), nil)
	if err != cl.SUCCESS {
		panic("failed to get kernel work group")
	}
	// ...then we start a single workgroup and execute the kernel on it (?), hence local == global?
	global := local
	err = cl.EnqueueNDRangeKernel(cq, kernel, 1, nil, &global, &local, 0, nil, nil)
	if err != cl.SUCCESS {
		panic("failed to execute kernel")
	}

	// this blocks until all queued commands complete
	cl.Finish(cq)

	results := make([]float32, DataSize)
	err = cl.EnqueueReadBuffer(cq, output, cl.TRUE, 0, 4*1024, unsafe.Pointer(&results[0]), 0, nil, nil)
	if err != nil {
		panic("failed to enqueue reading results")
	}

	fmt.Println(results)

	// and from here the results should just be correct
}
