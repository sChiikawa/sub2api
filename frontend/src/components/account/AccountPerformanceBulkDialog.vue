<template>
  <BaseDialog :show="true" :title="t('accountPerformance.bulk.title')" width="wide" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-700 dark:text-gray-300">{{ t('accountPerformance.bulk.selected', { count: accountIds.length }) }}</p>
      <p class="text-xs text-gray-500">{{ t('accountPerformance.bulk.hint') }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div class="flex flex-wrap items-center gap-4 text-sm">
        <label class="flex items-center gap-2"><input v-model="selected.enabled" type="checkbox" data-testid="bulk-field-enabled">{{ t('accountPerformance.bulk.modify') }}</label>
        <label class="flex items-center gap-2" :class="{ 'opacity-50': !selected.enabled }"><input v-model="enabled" type="checkbox" :disabled="!selected.enabled" data-testid="bulk-monitor-enabled">{{ t('accountPerformance.enabled') }}</label>
      </div>
      <table class="w-full text-left text-sm">
        <thead><tr><th class="w-16">{{ t('accountPerformance.bulk.modify') }}</th><th></th><th>{{ t('accountPerformance.min') }}</th><th>{{ t('accountPerformance.max') }}</th></tr></thead>
        <tbody><tr v-for="metric in metricKeys" :key="metric" :data-threshold="metric" :class="{ 'opacity-60': !selected[metric] }">
          <td class="p-1"><input v-model="selected[metric]" type="checkbox" :aria-label="t('accountPerformance.bulk.modify') + ' ' + t('accountPerformance.' + metric, { minutes: windowMinutes })" :data-testid="'bulk-field-' + metric"></td>
          <th class="py-2 pr-2">{{ t('accountPerformance.' + metric, { minutes: windowMinutes }) }} {{ metric==='ttft' ? '(' + t('accountPerformance.ms') + ')' : isPercentageMetric(metric)?'(%)':'' }}</th>
          <td v-for="bound in bounds" :key="bound" class="p-1"><input v-model="fields[metric][bound]" type="number" min="0" :max="isPercentageMetric(metric)?100:1000000000" step="any" :disabled="!selected[metric]" :aria-label="t('accountPerformance.'+metric, { minutes: windowMinutes })+' '+t('accountPerformance.'+bound)" :placeholder="t('accountPerformance.noBound')" class="input w-full min-w-0" :data-testid="'bulk-'+metric+'-'+bound"></td>
        </tr></tbody>
      </table>
      <p class="text-xs text-gray-500">{{ t('accountPerformance.semantics', { minutes: windowMinutes }) }}</p>
      <p class="rounded-lg bg-amber-50 p-3 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">{{ t('accountPerformance.latchNote') }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="busy" @click="emit('close')">{{ t('accountPerformance.close') }}</button>
      <button type="button" class="btn btn-primary" :disabled="busy||!chosenFields.length" data-testid="bulk-performance-save" @click="save">{{ t('accountPerformance.save') }}</button>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { emptyPerformanceConfig, type PerformanceBulkResult, type PerformanceConfigField } from '@/types/accountPerformance'
import BaseDialog from '@/components/common/BaseDialog.vue'
const props=defineProps<{accountIds:number[];windowMinutes:number}>()
const emit=defineEmits<{close:[];saved:[result:PerformanceBulkResult]}>()
const {t}=useI18n()
const metricKeys=['tps','degradation','ttft','upstream_error'] as const,bounds=['min','max'] as const
const selected=reactive<Record<PerformanceConfigField,boolean>>({enabled:false,tps:false,degradation:false,ttft:false,upstream_error:false})
const enabled=ref(true)
const fields=reactive<Record<typeof metricKeys[number],Record<typeof bounds[number],string|number>>>({tps:{min:'',max:''},degradation:{min:'',max:''},ttft:{min:'',max:''},upstream_error:{min:'',max:''}})
const busy=ref(false),error=ref('')
const chosenFields=computed(()=>(Object.keys(selected) as PerformanceConfigField[]).filter(key=>selected[key]))
const isPercentageMetric=(metric:typeof metricKeys[number])=>metric==='degradation'||metric==='upstream_error'
async function save(){
 const next=emptyPerformanceConfig();next.enabled=enabled.value
 for(const key of metricKeys){
  if(!selected[key])continue
  for(const bound of bounds){const raw=fields[key][bound];next[key][bound]=raw===''?null:Number(raw)}
  const {min,max}=next[key]
  if([min,max].some(n=>n!==null&&(!Number.isFinite(n)||n<0||n>(isPercentageMetric(key)?100:1e9)))||(min!==null&&max!==null&&min>max)){error.value=t('accountPerformance.invalid');return}
 }
 if(!chosenFields.value.length){error.value=t('accountPerformance.bulk.noFields');return}
 busy.value=true;error.value=''
 try{
  const result:PerformanceBulkResult={total:0,success:0,failed:0,errors:[]}
  for(let start=0;start<props.accountIds.length;start+=1000){
   const part=await adminAPI.accounts.bulkSavePerformance(props.accountIds.slice(start,start+1000),next,chosenFields.value)
   result.total+=part.total;result.success+=part.success;result.failed+=part.failed;result.errors.push(...(part.errors??[]))
  }
  emit('saved',result)
 }catch{error.value=t('accountPerformance.bulk.failed')}finally{busy.value=false}
}
</script>
