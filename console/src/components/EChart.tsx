import { useEffect, useRef } from 'react';
import * as echarts from 'echarts/core';
import { LineChart, BarChart, PieChart } from 'echarts/charts';
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkLineComponent,
  MarkAreaComponent,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import type { ECBasicOption } from 'echarts/types/dist/shared';

echarts.use([
  LineChart,
  BarChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkLineComponent,
  MarkAreaComponent,
  CanvasRenderer,
]);

interface Props {
  option: ECBasicOption;
  height?: number;
  className?: string;
}

export function EChart({ option, height = 260, className }: Props) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const instRef = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const chart = echarts.init(el, undefined, { renderer: 'canvas' });
    instRef.current = chart;

    const ro = new ResizeObserver(() => {
      chart.resize();
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      chart.dispose();
      instRef.current = null;
    };
  }, []);

  useEffect(() => {
    instRef.current?.setOption(option, { notMerge: true, lazyUpdate: true });
  }, [option]);

  return <div ref={hostRef} className={className} style={{ width: '100%', height }} />;
}

interface SparkProps {
  values: Array<number | null>;
  color: string;
  height?: number;
}

export function Sparkline({ values, color, height = 38 }: SparkProps) {
  const option: ECBasicOption = {
    animation: false,
    grid: { top: 4, right: 2, bottom: 2, left: 2 },
    xAxis: { type: 'category', show: false, data: values.map((_, i) => i) },
    yAxis: { type: 'value', show: false, scale: true },
    series: [
      {
        type: 'line',
        data: values,
        connectNulls: false,
        showSymbol: false,
        smooth: 0.25,
        lineStyle: { width: 2, color },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0, y: 0, x2: 0, y2: 1,
            colorStops: [
              { offset: 0, color: `${color}33` },
              { offset: 1, color: `${color}03` },
            ],
          },
        },
      },
    ],
  };
  return <EChart option={option} height={height} />;
}
