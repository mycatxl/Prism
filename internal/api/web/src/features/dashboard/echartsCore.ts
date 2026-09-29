import type { LineSeriesOption, MapSeriesOption } from "echarts/charts";
import type { GridComponentOption, TooltipComponentOption, VisualMapComponentOption } from "echarts/components";
import * as echarts from "echarts/core";
import { LineChart, MapChart } from "echarts/charts";
import { GridComponent, TooltipComponent, VisualMapComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import type { ComposeOption } from "echarts/core";

/**
 * The only module that imports ECharts.
 *
 * Registration is explicit, so the bundler ships the two chart types this screen
 * uses instead of the whole library — and because only the lazily loaded chart
 * components import this file, nothing else in the panel pays for it.
 */
echarts.use([LineChart, MapChart, GridComponent, TooltipComponent, VisualMapComponent, CanvasRenderer]);

export { echarts };

export type ChartOption = ComposeOption<
  | LineSeriesOption
  | MapSeriesOption
  | GridComponentOption
  | TooltipComponentOption
  | VisualMapComponentOption
>;

export type EChartsInstance = ReturnType<typeof echarts.init>;