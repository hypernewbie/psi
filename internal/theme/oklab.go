// Package theme includes a port of Pi's OKHSL color conversion, which in turn
// is the reference implementation by Björn Ottosson. Copyright (c) 2021 Björn
// Ottosson. Used under the MIT license; the original copyright and permission
// notice below apply to this file.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to
// deal in the Software without restriction, including without limitation the
// rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
// sell copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
package theme

import (
	"math"
)

type vector [3]float64
type matrix [3]vector

func multiply(m matrix, v vector) vector {
	return vector{
		m[0][0]*v[0] + m[0][1]*v[1] + m[0][2]*v[2],
		m[1][0]*v[0] + m[1][1]*v[1] + m[1][2]*v[2],
		m[2][0]*v[0] + m[2][1]*v[1] + m[2][2]*v[2],
	}
}

var linearSRGBToLMS = matrix{
	{0.4122214694707629, 0.5363325372617349, 0.0514459932675022},
	{0.2119034958178251, 0.6806995506452344, 0.1073969535369405},
	{0.0883024591900564, 0.2817188391361215, 0.6299787016738222},
}
var lmsToLab = matrix{
	{0.210454268309314, 0.793617774702305, -0.0040720430116193},
	{1.9779985324311684, -2.42859224204858, 0.450593709617411},
	{0.0259040424655478, 0.7827717124575296, -0.8086757549230774},
}
var labToLMS = matrix{
	{1, 0.3963377773761749, 0.2158037573099136},
	{1, -0.1055613458156586, -0.0638541728258133},
	{1, -0.0894841775298119, -1.2914855480194092},
}
var lmsToLinearSRGB = matrix{
	{4.0767416360759583, -3.3077115392580629, 0.2309699031821043},
	{-1.2684379732850315, 2.6097573492876882, -0.341319376002657},
	{-0.0041960761386756, -0.7034186179359362, 1.7076146940746117},
}

var saturationFit = []struct {
	halfPlane [2]float64
	coeff     [5]float64
}{
	{[2]float64{-1.8817031, -0.80936501}, [5]float64{1.19086277, 1.76576728, 0.59662641, 0.75515197, 0.56771245}},
	{[2]float64{1.8144408, -1.19445267}, [5]float64{0.73956515, -0.45954404, 0.08285427, 0.12541073, -0.14503204}},
	{[2]float64{0.13110758, 1.81333971}, [5]float64{1.35733652, -0.00915799, -1.1513021, -0.50559606, 0.00692167}},
}

const oklabK1 = 0.206
const oklabK2 = 0.03
const oklabK3 = (1 + oklabK1) / (1 + oklabK2)

func oklabToOkhslLightness(x float64) float64 {
	return 0.5 * (oklabK3*x - oklabK1 + math.Sqrt(math.Pow(oklabK3*x-oklabK1, 2)+4*oklabK2*oklabK3*x))
}
func okhslToOklabLightness(x float64) float64 {
	return (x*x + oklabK1*x) / (oklabK3 * (x + oklabK2))
}
func linearToSRGB(value float64) float64 {
	if value > 0.0031308 {
		return 1.055*math.Pow(value, 1/2.4) - 0.055
	}
	return 12.92 * value
}
func srgbToLinear(value float64) float64 {
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func oklabToLinearSRGB(lab vector) vector {
	lms := multiply(labToLMS, lab)
	return multiply(lmsToLinearSRGB, vector{lms[0] * lms[0] * lms[0], lms[1] * lms[1] * lms[1], lms[2] * lms[2] * lms[2]})
}
func linearSRGBToOklab(rgb vector) vector {
	lms := multiply(linearSRGBToLMS, rgb)
	return multiply(lmsToLab, vector{math.Cbrt(lms[0]), math.Cbrt(lms[1]), math.Cbrt(lms[2])})
}
func rgbToOklab(rgb RGB) vector {
	return linearSRGBToOklab(vector{srgbToLinear(rgb.R / 255), srgbToLinear(rgb.G / 255), srgbToLinear(rgb.B / 255)})
}
func linearSRGBToRGB(linear vector) RGB {
	channel := func(value float64) float64 { return math.Round(math.Min(1, math.Max(0, linearToSRGB(value))) * 255) }
	return RGB{R: channel(linear[0]), G: channel(linear[1]), B: channel(linear[2])}
}

func lmsSlopes(a, b float64) vector {
	return vector{
		labToLMS[0][1]*a + labToLMS[0][2]*b,
		labToLMS[1][1]*a + labToLMS[1][2]*b,
		labToLMS[2][1]*a + labToLMS[2][2]*b,
	}
}
func maxSaturation(a, b float64) float64 {
	channel := 2
	for i := 0; i < 2; i++ {
		if saturationFit[i].halfPlane[0]*a+saturationFit[i].halfPlane[1]*b > 1 {
			channel = i
			break
		}
	}
	k := saturationFit[channel].coeff
	weights := lmsToLinearSRGB[channel]
	saturation := k[0] + k[1]*a + k[2]*b + k[3]*a*a + k[4]*a*b
	slopes := lmsSlopes(a, b)
	base := vector{1 + saturation*slopes[0], 1 + saturation*slopes[1], 1 + saturation*slopes[2]}
	dot := func(values vector) float64 { return weights[0]*values[0] + weights[1]*values[1] + weights[2]*values[2] }
	f := dot(vector{base[0] * base[0] * base[0], base[1] * base[1] * base[1], base[2] * base[2] * base[2]})
	f1 := dot(vector{3 * slopes[0] * base[0] * base[0], 3 * slopes[1] * base[1] * base[1], 3 * slopes[2] * base[2] * base[2]})
	f2 := dot(vector{6 * slopes[0] * slopes[0] * base[0], 6 * slopes[1] * slopes[1] * base[1], 6 * slopes[2] * slopes[2] * base[2]})
	return saturation - f*f1/(f1*f1-0.5*f*f2)
}
func cusp(a, b float64) (float64, float64) {
	saturation := maxSaturation(a, b)
	linear := oklabToLinearSRGB(vector{1, saturation * a, saturation * b})
	lightness := math.Cbrt(1 / math.Max(linear[0], math.Max(linear[1], linear[2])))
	return lightness, lightness * saturation
}
func maxChroma(a, b, lightness, cuspL, cuspC float64) float64 {
	if lightness <= cuspL {
		return cuspC * lightness / cuspL
	}
	t := cuspC * (lightness - 1) / (cuspL - 1)
	slopes := lmsSlopes(a, b)
	lms := vector{lightness + t*slopes[0], lightness + t*slopes[1], lightness + t*slopes[2]}
	cubes := vector{math.Pow(lms[0], 3), math.Pow(lms[1], 3), math.Pow(lms[2], 3)}
	first := vector{3 * slopes[0] * lms[0] * lms[0], 3 * slopes[1] * lms[1] * lms[1], 3 * slopes[2] * lms[2] * lms[2]}
	second := vector{6 * slopes[0] * slopes[0] * lms[0], 6 * slopes[1] * slopes[1] * lms[1], 6 * slopes[2] * slopes[2] * lms[2]}
	dot := func(row vector, values vector) float64 { return row[0]*values[0] + row[1]*values[1] + row[2]*values[2] }
	steps := [3]float64{}
	for i, row := range lmsToLinearSRGB {
		f := dot(row, cubes) - 1
		f1 := dot(row, first)
		f2 := dot(row, second)
		u := f1 / (f1*f1 - 0.5*f*f2)
		steps[i] = math.MaxFloat64
		if u >= 0 {
			steps[i] = -f * u
		}
	}
	return t + math.Min(steps[0], math.Min(steps[1], steps[2]))
}
func chromaStops(L, a, b float64) (float64, float64, float64) {
	peakL, peakC := cusp(a, b)
	cMax := maxChroma(a, b, L, peakL, peakC)
	k := cMax / math.Min(L*(peakC/peakL), (1-L)*(peakC/(1-peakL)))
	midS := 0.11516993 + 1/(7.4477897+4.1590124*b+a*(-2.19557347+1.75198401*b+a*(-2.13704948-10.02301043*b+a*(-4.24894561+5.38770819*b+4.69891013*a))))
	midT := 0.11239642 + 1/(1.6132032-0.68124379*b+a*(0.40370612+0.90148123*b+a*(-0.27087943+0.6122399*b+a*(0.00299215-0.45399568*b-0.14661872*a))))
	cMid := 0.9 * k * math.Sqrt(math.Sqrt(1/(1/math.Pow(L*midS, 4)+1/math.Pow((1-L)*midT, 4))))
	c0 := math.Sqrt(1 / (1/math.Pow(L*0.4, 2) + 1/math.Pow((1-L)*0.8, 2)))
	return c0, cMid, cMax
}

func okhslToRGB(hue, saturation, lightness float64) RGB {
	L := okhslToOklabLightness(lightness)
	lab := vector{L, 0, 0}
	if L > 0 && L < 1 && saturation > 0 {
		angle := 2 * math.Pi * positiveModulo(hue, 360) / 360
		a, b := math.Cos(angle), math.Sin(angle)
		c0, cMid, cMax := chromaStops(L, a, b)
		var chroma float64
		if saturation < 0.8 {
			t := 1.25 * saturation
			k1 := 0.8 * c0
			chroma = t * k1 / (1 - (1-k1/cMid)*t)
		} else {
			t := 5 * (saturation - 0.8)
			k1 := 0.2 * cMid * cMid * 1.25 * 1.25 / c0
			chroma = cMid + t*k1/(1-(1-k1/(cMax-cMid))*t)
		}
		lab = vector{L, chroma * a, chroma * b}
	}
	return linearSRGBToRGB(oklabToLinearSRGB(lab))
}
func rgbToOKHSL(rgb RGB) (hue, saturation, lightness float64) {
	lab := rgbToOklab(rgb)
	chroma := math.Hypot(lab[1], lab[2])
	lightness = oklabToOkhslLightness(lab[0])
	if chroma < 1e-9 || lightness <= 0 || lightness >= 1 {
		return 0, 0, lightness
	}
	hue = positiveModulo(math.Atan2(lab[2], lab[1])*180/math.Pi, 360)
	c0, cMid, cMax := chromaStops(lab[0], lab[1]/chroma, lab[2]/chroma)
	if chroma < cMid {
		k1 := 0.8 * c0
		saturation = 0.8 * (chroma / (k1 + (1-k1/cMid)*chroma))
	} else {
		k1 := 0.2 * cMid * cMid * 1.25 * 1.25 / c0
		offset := chroma - cMid
		saturation = 0.8 + 0.2*(offset/(k1+(1-k1/(cMax-cMid))*offset))
	}
	return hue, math.Min(1, math.Max(0, saturation)), lightness
}

func positiveModulo(value, modulus float64) float64 {
	return math.Mod(math.Mod(value, modulus)+modulus, modulus)
}
