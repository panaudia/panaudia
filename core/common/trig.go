package common

import (
	"math"
)

const DEGREES_TO_RADIANS = math.Pi / 180.0
const RADIANS_TO_DEGREES = 180.0 / math.Pi
const PI_4 = math.Pi / 4.0

var SQRT_3_2 = float32(math.Sqrt(3.0) / 2.0)
var SQRT_15_2 = float32(math.Sqrt(15.0) / 2.0)
var SQRT_5_8 = float32(math.Sqrt(5.0 / 8.0))
var SQRT_3_8 = float32(math.Sqrt(3.0 / 8.0))

func TrigCartesianNormRectAndDistance(from Position, to Position) (Position, float64) {
	rect := Position{to.X - from.X, to.Y - from.Y, to.Z - from.Z}
	distance := math.Sqrt((rect.X * rect.X) + (rect.Y * rect.Y) + (rect.Z * rect.Z))
	if distance < 1e-10 {
		// Coincident positions: return forward direction and minimal distance
		return Position{0, 1, 0}, 1e-10
	}
	norm := Position{rect.X / distance, rect.Y / distance, rect.Z / distance}
	return norm, distance
}

func TrigCartesianRelativePosition(from Position, to Position) Position {
	return Position{to.X - from.X, to.Y - from.Y, to.Z - from.Z}
}

func TrigCartesianSumPosition(one Position, two Position) Position {
	return Position{one.X + two.X, one.Y + two.Y, one.Z + two.Z}
}

//func TrigCartesianToPolar(pos Position) PolarPosition {
//	hypo1 := (pos.X * pos.X) + (pos.Y * pos.Y)
//	a := -(math.Atan2(pos.X, pos.Y) * RADIANS_TO_DEGREES)
//	e := math.Atan2(pos.Z, hypo1) * RADIANS_TO_DEGREES
//	d := math.Hypot(hypo1, pos.Z)
//	if a < -180.0 {
//		a = a + 360.0
//	}
//	if a > 180.0 {
//		a = a - 360.0
//	}
//	if e < -90.0 {
//		e = e + 180.0
//	}
//	if e > 90.0 {
//		e = e - 180.0
//	}
//	return PolarPosition{a, e, d}
//}

func TrigPolarToCartesian(polar PolarPosition) Position {
	a := polar.Azimuth * DEGREES_TO_RADIANS
	e := polar.Elevation * DEGREES_TO_RADIANS
	z := math.Sin(e) * polar.Distance
	l := math.Sqrt(polar.Distance*polar.Distance - z*z)
	x := -math.Sin(a) * l
	y := math.Cos(a) * l
	return Position{x, y, z}
}

//func TrigCartesianToPolar(pos Position) PolarPosition {
//	xxyy := (pos.X * pos.X) + (pos.Y * pos.Y)
//	xxyyzz := xxyy + (pos.Z * pos.Z)
//	a := -(math.Atan2(pos.X, pos.Y) * RADIANS_TO_DEGREES)
//	e := math.Atan2(pos.Z, math.Sqrt(xxyy)) * RADIANS_TO_DEGREES
//	d := math.Sqrt(xxyyzz)
//	if a < -180.0 {
//		a = a + 360.0
//	}
//	if a > 180.0 {
//		a = a - 360.0
//	}
//	if e < -90.0 {
//		e = e + 180.0
//	}
//	if e > 90.0 {
//		e = e - 180.0
//	}
//	//println("a: %v", a)
//	return PolarPosition{a, e, d}
//}

func TrigCartesianToPolar(pos Position) PolarPosition {
	xxyy := (pos.X * pos.X) + (pos.Y * pos.Y)
	xxyyzz := xxyy + (pos.Z * pos.Z)
	a := math.Atan2(pos.Y, pos.X) * RADIANS_TO_DEGREES
	e := math.Atan2(pos.Z, math.Sqrt(xxyy)) * RADIANS_TO_DEGREES

	d := math.Sqrt(xxyyzz)
	if a < -180.0 {
		a = a + 360.0
	}
	if a > 180.0 {
		a = a - 360.0
	}
	if e < -90.0 {
		e = e + 180.0
	}
	if e > 90.0 {
		e = e - 180.0
	}
	//println("a: %v", a)
	return PolarPosition{a, e, d}
}

func TrigCartesianDistance(pos Position) float64 {
	return math.Sqrt((pos.X * pos.X) + (pos.Y * pos.Y) + (pos.Z * pos.Z))
}

func TrigCartesianToPolarApprox(pos Position) PolarPosition {
	xxyy := (pos.X * pos.X) + (pos.Y * pos.Y)
	xxyyzz := xxyy + (pos.Z * pos.Z)
	a := -(ApproxAtan(pos.X, pos.Y) * RADIANS_TO_DEGREES)
	//a := 0.0
	e := ApproxAtan(pos.Z, math.Sqrt(xxyy)) * RADIANS_TO_DEGREES
	//e := 0.0
	d := math.Sqrt(xxyyzz)
	if a < -180.0 {
		a = a + 360.0
	}
	if a > 180.0 {
		a = a - 360.0
	}
	if e < -90.0 {
		e = e + 180.0
	}
	if e > 90.0 {
		e = e - 180.0
	}
	return PolarPosition{a, e, d}
}

func ApproxFastAtan(y float64, x float64) float64 {

	l := min(math.Abs(x), math.Abs(y)) / max(math.Abs(x), math.Abs(y))

	a := PI_4*l - l*(math.Abs(l)-1)*(0.2447+0.0663*math.Abs(l))

	return a
}

//double FastArcTan(double x)
//{
//return M_PI_4*x - x*(fabs(x) - 1)*(0.2447 + 0.0663*fabs(x));
//}

func ApproxAtan(y float64, x float64) float64 {

	l := min(math.Abs(x), math.Abs(y)) / max(math.Abs(x), math.Abs(y))
	s := l * l
	a := ((-0.0464964749*s+0.15931422)*s-0.327622764)*s*l + l

	if math.Abs(y) > math.Abs(x) {
		a = 1.57079637 - a
	}
	if x < 0 {
		a = 3.14159274 - a
	}
	if y < 0 {
		a = -a
	}
	return a

	//a := min (|x|, |y|) / max (|x|, |y|)
	//s := a * a
	//r := ((-0.0464964749 * s + 0.15931422) * s - 0.327622764) * s * a + a
	//if |y| > |x| then r := 1.57079637 - r
	//if x < 0 then r := 3.14159274 - r
	//if y < 0 then r := -r
}

func TrigCartesianInverse(pos Position) Position {
	return Position{-pos.X, -pos.Y, -pos.Z}
}

func TrigCartesianScaled(pos Position, scale float64) Position {
	return Position{pos.X * scale, pos.Y * scale, pos.Z * scale}
}

func TrigPolarInverse(polar PolarPosition) PolarPosition {
	az := polar.Azimuth - 180.0
	if az < -180.0 {
		az = az + 360.0
	}
	return PolarPosition{az, -polar.Elevation, polar.Distance}
}

func FromToToPolar(from Position, to Position) PolarPosition {
	return TrigCartesianToPolar(TrigCartesianRelativePosition(from, to))
}

//func FromToToPolarFixed(from Position, to Position) PolarPosition {
//	return TrigCartesianToPolar(TrigCartesianRelativePosition(from, to))
//}

//func TrigPolarFromToScaled(from Position, to Position, scale float64) PolarPosition {
//	relativePosition := TrigCartesianRelativePosition(from, to)
//	relativePositionScaled := TrigCartesianScaled(relativePosition, scale)
//	return TrigCartesianToPolar(relativePositionScaled)
//}

//func MakePolarLookup() []float64 {
//	inputRange := 1024.0 * 1024.0
//	maxy := inputRange
//	maxx := (math.Sqrt(2.0) * maxy)
//
//	indexx_range := int(math.Sqrt(maxx)) + 1
//	indexy_range := 1025
//
//	lookup := make([]float64, indexx_range*indexy_range*2)
//
//	for x := 0; x < indexx_range; x++ {
//		for y := 0; y < indexy_range; y++ {
//			xf := float64(x)
//			yf := float64(y)
//			i := polarLookupPosition(x, y)
//			a, d := RectToPolar(inputRange*xf, inputRange*yf)
//			//fmt.Printf("x: %d y %d\n", x, y)
//			lookup[i] = a
//			lookup[i+1] = d / inputRange
//		}
//	}
//	return lookup
//}

func RectToPolar(x float64, y float64) (a float64, d float64) {
	a = math.Atan2(y, x) * RADIANS_TO_DEGREES
	d = math.Sqrt((x * x) + (y * y))
	if a < -180.0 {
		a = a + 360.0
	}
	if a > 180.0 {
		a = a - 360.0
	}
	if a == 360.0 {
		a = 0
	}
	return
}
