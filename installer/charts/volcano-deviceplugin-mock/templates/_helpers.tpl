{{/*
Expand the name of the chart.
*/}}
{{- define "volcano-deviceplugin-mock.name" -}}
{{- default .Chart.Name .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Compatibility mock for D910C/D950; this does not claim physical D950 wiring. */}}
{{- define "volcano-deviceplugin-mock.ascendHCCSAndSIOPatch" -}}
metadata:
  annotations:{{ `
    {{- $num_list := atoi .capacity | until }}
    {{- $hccs_list := join "," $num_list | list }}
    {{- $sio_list := list }}
    {{- range $i, $e := chunk 2 $num_list }}
    {{- $sio_list = join "," $e | append $sio_list }}
    {{- end }}
    volcano.sh/device-topologys: '{"topologys":[{"type":"HCCS","groups":{{ toJson $hccs_list }}},{"type":"SIO","groups":{{ toJson $sio_list }}}]}'` }}
{{- end }}

{{- define "volcano-deviceplugin-mock.ascend1980UndoPatch" -}}
metadata:
  annotations:
    volcano.sh/device-topologys: null
    huawei.com/ascend-1980-status: null
    huawei.com/ascend-status: null
{{- end }}

{{- define "volcano-deviceplugin-mock.controller.name" -}}
{{ include "volcano-deviceplugin-mock.name" . }}-controller
{{- end }}

{{- define "volcano-deviceplugin-mock.daemon.name" -}}
{{ include "volcano-deviceplugin-mock.name" . }}-daemon
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "volcano-deviceplugin-mock.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "volcano-deviceplugin-mock.labels" -}}
helm.sh/chart: {{ include "volcano-deviceplugin-mock.chart" . }}
{{ include "volcano-deviceplugin-mock.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "volcano-deviceplugin-mock.selectorLabels" -}}
app.kubernetes.io/name: {{ include "volcano-deviceplugin-mock.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
