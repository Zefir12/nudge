import { computed } from 'vue';
import { useRoute } from 'vue-router';

// useParams exposes the route's :env and :id as strings.
export function useParams() {
  const route = useRoute();
  const param = (name: string) => {
    const v = route.params[name];
    return typeof v === 'string' ? v : '';
  };
  return { env: computed(() => param('env')), id: computed(() => param('id')) };
}
