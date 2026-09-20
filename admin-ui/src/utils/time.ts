import dayjs from 'dayjs';
import 'dayjs/locale/zh-cn';

dayjs.locale('zh-cn');

export function formatTime(value?: string | null): string {
  if (!value) return '-';
  return dayjs(value).format('YYYY-MM-DD HH:mm:ss');
}

export function formatDate(value?: string | null): string {
  if (!value) return '-';
  return dayjs(value).format('YYYY-MM-DD');
}

export function toRFC3339(value: dayjs.Dayjs | null): string | undefined {
  if (!value) return undefined;
  return value.toISOString();
}
