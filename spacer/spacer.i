%module spacer

%typemap(gotype) (const float *const *) "uintptr"
%typemap(gotype) (float* const*) "uintptr"
%typemap(gotype) (float* const* const) "uintptr"

%{
#include "../../Spatial_Audio_Framework/examples/include/ambi_bin.h"
#include "../../panaudia-utils/include/panaudia_utils.h"
%}

%include "../../Spatial_Audio_Framework/examples/include/ambi_bin.h"
%include "../../panaudia-utils/include/panaudia_utils.h"
