<template>
  <AppLayout>
    <div class="space-y-6">
      <SmartOpsNav />
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.description') }}</p>
        </div>
        <div class="flex items-center gap-3">
          <label class="inline-flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input v-model="autoRefresh" type="checkbox" class="rounded border-gray-300 text-primary-600" />
            {{ t('admin.harvestFlow.autoRefresh') }}
          </label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || saving" @click="fetchFlow">
            <Icon name="refresh" size="sm" class="mr-1" :class="{ 'animate-spin': loading || refreshing }" />
            {{ t('common.refresh') }}
          </button>
        </div>
      </div>

      <div v-if="errorMessage" class="rounded-2xl bg-red-50 p-4 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">
        {{ errorMessage }}
      </div>

      <HarvestPolicyPanel @saved="fetchFlow" />
      <HarvestControlsPanel
        :refresh-key="snapshot?.generated_at"
        :runtime="snapshot?.runtime"
        :accounts="snapshot?.accounts || []"
        :models="snapshot?.harvest.models || []"
        @saved="fetchFlow"
        @running="harvesting = $event"
        @finished="fetchFlow"
      />

      <div v-if="loading && !snapshot" class="flex items-center justify-center py-16">
        <LoadingSpinner />
      </div>

      <template v-else-if="snapshot">
        <div class="card overflow-hidden p-5">
          <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span class="inline-flex items-center gap-1 rounded-full px-2 py-0.5" :class="snapshot.harvest.enabled ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-gray-100 text-gray-500 dark:bg-dark-700'">
                {{ snapshot.harvest.enabled ? t('admin.harvestFlow.harvestOn') : t('admin.harvestFlow.harvestOff') }}
              </span>
              <span class="inline-flex items-center gap-1 rounded-full px-2 py-0.5" :class="snapshot.harvest.fail_closed ? 'bg-rose-50 text-rose-700 dark:bg-rose-900/30 dark:text-rose-300' : 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'">
                {{ snapshot.harvest.fail_closed ? t('admin.harvestFlow.failClosed') : t('admin.harvestFlow.failOpen') }}
              </span>
              <span>{{ t('admin.harvestFlow.strategy') }} {{ snapshot.harvest.strategy }}</span>
              <span>{{ t('admin.harvestFlow.scope') }} {{ scopeLabel(snapshot.harvest.scope_mode, snapshot.harvest.account_policy, snapshot.harvest.group_ids) }}</span>
              <span>{{ t('admin.harvestFlow.interval') }} {{ t('admin.harvestFlow.seconds', { n: snapshot.harvest.probe_interval_seconds }) }}</span>
              <span>{{ t('admin.harvestFlow.cooldown') }} {{ t('admin.harvestFlow.seconds', { n: snapshot.harvest.cooldown_seconds }) }}</span>
              <span v-if="snapshot.harvest.models?.length">{{ t('admin.harvestFlow.models') }} {{ snapshot.harvest.models.join(' / ') }}</span>
            </div>
            <p v-if="lastUpdated" class="text-xs text-gray-400">{{ t('admin.harvestFlow.lastUpdated', { time: lastUpdated }) }}</p>
          </div>

          <ol class="grid grid-cols-1 gap-3 md:grid-cols-5">
            <li v-for="(stage, index) in snapshot.stages" :key="stage.id" class="relative">
              <div class="rounded-2xl border p-4 transition-colors" :class="stageCardClass(stage.status)">
                <div class="mb-3 flex items-center justify-between">
                  <div class="flex h-9 w-9 items-center justify-center rounded-xl" :class="stageIconClass(stage.status)">
                    <Icon :name="stageIcon(stage.id)" size="sm" />
                  </div>
                  <span class="text-[11px] font-medium uppercase tracking-wide" :class="stageTextClass(stage.status)">
                    {{ statusLabel(stage.status) }}
                  </span>
                </div>
                <p class="text-sm font-semibold text-gray-900 dark:text-white">{{ t(`admin.harvestFlow.stages.${stage.id}`) }}</p>
                <p class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">{{ stageDetail(stage) }}</p>
                <p v-if="stage.model || stage.at || stage.http_status" class="mt-2 text-[11px] text-gray-400">
                  <span v-if="stage.model">{{ stage.model }}</span>
                  <span v-if="stage.http_status"> · HTTP {{ stage.http_status }}</span>
                  <span v-if="(stage.model || stage.http_status) && stage.at"> · </span>
                  <span v-if="stage.at">{{ stage.id === 'ticket' ? t('admin.harvestFlow.latestTicketEvent', { time: formatClock(stage.at) }) : formatClock(stage.at) }}</span>
                </p>
              </div>
              <div v-if="index < snapshot.stages.length - 1" class="pointer-events-none absolute right-[-10px] top-1/2 hidden h-px w-5 bg-gradient-to-r from-gray-300 to-transparent md:block dark:from-dark-600" />
            </li>
          </ol>
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-5">
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.sidecar') }}</p>
            <p v-if="snapshot.sidecar.mode === 'external'" class="mt-1 text-sm font-semibold text-gray-600 dark:text-gray-300">
              {{ t('admin.harvestFlow.externalProxy') }}
            </p>
            <p v-else-if="snapshot.sidecar.mode === 'unconfigured'" class="mt-1 text-sm text-gray-500">{{ t('admin.harvestFlow.proxyUnconfigured') }}</p>
            <p v-else class="mt-1 text-sm font-semibold" :class="snapshot.sidecar.reachable ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'">
              {{ snapshot.sidecar.reachable ? t('admin.harvestFlow.sidecarReachable') : t('admin.harvestFlow.sidecarOffline') }}
            </p>
            <p v-if="snapshot.sidecar.mode === 'external'" class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.externalProxyHint') }}</p>
            <template v-else-if="snapshot.sidecar.mode !== 'unconfigured'">
              <p class="mt-2 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
                {{ snapshot.sidecar.now || (snapshot.sidecar.reachable ? t('admin.harvestFlow.poolOnline', { n: snapshot.sidecar.all_count || 0 }) : t('admin.harvestFlow.waitingSidecar')) }}
              </p>
              <p class="mt-1 text-xs text-gray-400">{{ t('admin.harvestFlow.nodePool') }} {{ snapshot.sidecar.all_count || 0 }} · {{ snapshot.sidecar.group || 'CODEX-ROTATE' }}</p>
              <p v-if="snapshot.sidecar.error" class="mt-1 text-xs text-rose-500">{{ snapshot.sidecar.error }}</p>
            </template>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.ready') }}</p>
            <p class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ snapshot.counts.tickets_ready }}</p>
            <p class="text-xs text-rose-500">{{ t('admin.harvestFlow.paused') }} {{ snapshot.counts.tickets_blocked }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.probeHit') }} / {{ t('admin.harvestFlow.probeMiss') }}</p>
            <p class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ snapshot.counts.probe_hit }} / {{ snapshot.counts.probe_miss }}</p>
            <p class="text-xs text-gray-400">{{ t('admin.harvestFlow.ticketAccept') }} {{ snapshot.counts.ticket_accept }} · {{ t('admin.harvestFlow.ticketReject') }} {{ snapshot.counts.ticket_reject }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.stages.select') }}</p>
            <p class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{{ snapshot.counts.select_ok }} / {{ snapshot.counts.select_skip }}</p>
            <p class="text-xs text-gray-400">{{ t('admin.harvestFlow.selectOk') }} / {{ t('admin.harvestFlow.selectSkip') }}</p>
            <p v-if="snapshot.harvest.harvest_proxy" class="mt-2 truncate font-mono text-[11px] text-gray-400">{{ snapshot.harvest.harvest_proxy }}</p>
          </div>
          <HarvestCoverageCard :coverage="snapshot.coverage" :generated-at="snapshot.generated_at" />
        </div>

        <div class="card p-5" data-testid="gateway-pool">
          <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.gatewayPool') }}</h2>
            <div class="flex flex-wrap items-center gap-3">
              <span class="text-xs text-gray-400">{{ t('admin.harvestFlow.gatewayPoolCount', { unique: gatewayUniqueCount, total: snapshot.gateways?.length || 0 }) }}</span>
              <button
                type="button"
                data-testid="gateway-archive-expired"
                class="btn btn-secondary btn-sm"
                :disabled="archivingGateways || !gatewayExpiredCount"
                @click="archiveExpiredGateways"
              >
                {{ t('admin.harvestFlow.gatewayArchiveExpired', { n: gatewayExpiredCount }) }}
              </button>
            </div>
          </div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.harvestFlow.gatewayPoolHint') }}</p>
          <div class="mt-3 rounded-xl border border-gray-100 p-3 dark:border-dark-700" data-testid="gateway-turns">
            <h3 class="text-xs font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.gatewayTurns') }}</h3>
            <p v-if="!snapshot.gateway_turns?.length" class="mt-1 text-xs text-amber-600 dark:text-amber-400">{{ t('admin.harvestFlow.gatewayTurnsEmpty') }}</p>
            <ul v-else class="mt-2 space-y-2">
              <li v-for="turn in snapshot.gateway_turns" :key="turn.account_id" class="text-xs text-gray-600 dark:text-gray-300">
                <span class="font-medium text-gray-900 dark:text-white">#{{ turn.account_id }} {{ gatewayTurnName(turn) }}</span>
                <span class="mt-0.5 block font-mono text-[11px]">{{ turn.host }}</span>
                <span class="mt-0.5 block">{{ gatewayTurnRemainText(turn) }}</span>
                <span class="mt-0.5 block text-[11px] text-gray-400">{{ t('admin.harvestFlow.gatewayTurnUsed', { hosts: (turn.used_hosts || []).join('、') }) }}</span>
              </li>
            </ul>
          </div>
          <p v-if="!snapshot.gateways?.length" class="mt-3 text-sm text-gray-500">{{ t('admin.harvestFlow.gatewayPoolEmpty') }}</p>
          <div v-else class="mt-3 overflow-x-auto">
            <table class="min-w-full text-left text-sm">
              <tbody>
                <template v-for="group in gatewayGroups" :key="group.host">
                  <tr v-for="gateway in group.rows" :key="`${gateway.host}-${gateway.account_id || 0}-${gateway.order}`" class="border-t border-gray-100 dark:border-dark-700">
                    <td class="py-2 pr-3 align-middle">
                      <span class="break-all font-mono text-xs text-gray-800 dark:text-gray-200">{{ gateway.host }}</span>
                      <span v-if="gateway.account_id" class="mt-0.5 block text-[11px] text-gray-400">#{{ gateway.account_id }} {{ gateway.account_name }}</span>
                    </td>
                    <td class="whitespace-nowrap py-2 pr-3 align-middle">
                      <span class="rounded-full px-2 py-0.5 text-[11px]" :class="gatewayStatusClass(gateway.status)">{{ gatewayStatusLabel(gateway.status) }}</span>
                      <span v-if="gateway.status === 'retired'" class="ml-2 text-[11px] text-rose-500">{{ t('admin.harvestFlow.gatewayCredentialExpired') }}</span>
                    </td>
                    <td class="whitespace-nowrap py-2 pr-3 align-middle text-xs text-gray-600 dark:text-gray-300">{{ gatewayRemainText(gateway) }}</td>
                    <td class="py-2 pr-3 align-middle">
                      <div class="h-1.5 w-28 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                        <div class="h-1.5 rounded-full" :class="gatewayBarClass(gateway)" :style="{ width: gatewayBarWidth(gateway) }" />
                      </div>
                    </td>
                    <td class="whitespace-nowrap py-2 text-right align-middle text-xs text-gray-400">{{ formatClock(gateway.saved_at) }}</td>
                  </tr>
                  <tr v-if="group.backups.length" class="border-t border-gray-50 dark:border-dark-800">
                    <td colspan="5" class="py-1.5">
                      <button type="button" class="text-xs text-sky-700 dark:text-sky-300" data-testid="gateway-backups" :aria-expanded="!!expandedGatewayHosts[group.host]" @click="toggleGatewayBackups(group.host)">
                        {{ expandedGatewayHosts[group.host] ? t('admin.harvestFlow.gatewayBackupsHide', { n: group.backups.length }) : t('admin.harvestFlow.gatewayBackups', { n: group.backups.length }) }}
                      </button>
                    </td>
                  </tr>
                  <template v-if="expandedGatewayHosts[group.host]">
                    <tr v-for="gateway in group.backups" :key="`${gateway.host}-${gateway.account_id || 0}-${gateway.order}`" class="border-t border-gray-50 bg-gray-50/60 dark:border-dark-800 dark:bg-dark-800/40">
                      <td class="py-2 pr-3 pl-4 align-middle">
                        <span class="break-all font-mono text-xs text-gray-500 dark:text-gray-400">{{ gateway.host }}</span>
                        <span v-if="gateway.account_id" class="mt-0.5 block text-[11px] text-gray-400">#{{ gateway.account_id }} {{ gateway.account_name }}</span>
                      </td>
                      <td class="whitespace-nowrap py-2 pr-3 align-middle">
                        <span class="rounded-full px-2 py-0.5 text-[11px]" :class="gatewayStatusClass(gateway.status)">{{ gatewayStatusLabel(gateway.status) }}</span>
                        <span class="ml-2 rounded-full bg-sky-50 px-2 py-0.5 text-[11px] text-sky-700 dark:bg-sky-900/30 dark:text-sky-300">{{ t('admin.harvestFlow.gatewayBackup') }}</span>
                        <span v-if="gateway.status === 'retired'" class="ml-2 text-[11px] text-rose-500">{{ t('admin.harvestFlow.gatewayCredentialExpired') }}</span>
                      </td>
                      <td class="whitespace-nowrap py-2 pr-3 align-middle text-xs text-gray-600 dark:text-gray-300">{{ gatewayRemainText(gateway) }}</td>
                      <td class="py-2 pr-3 align-middle">
                        <div class="h-1.5 w-28 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                          <div class="h-1.5 rounded-full" :class="gatewayBarClass(gateway)" :style="{ width: gatewayBarWidth(gateway) }" />
                        </div>
                      </td>
                      <td class="whitespace-nowrap py-2 text-right align-middle text-xs text-gray-400">{{ formatClock(gateway.saved_at) }}</td>
                    </tr>
                  </template>
                </template>
              </tbody>
            </table>
          </div>
        </div>

        <div class="grid grid-cols-1 gap-6 xl:grid-cols-5">
          <div class="card p-5 xl:col-span-2">
            <h2 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.accounts') }}</h2>
            <div v-if="!snapshot.accounts?.length" class="text-sm text-gray-500">{{ t('admin.harvestFlow.noAccounts') }}</div>
            <div v-else class="grid grid-cols-1 gap-3">
              <div
                v-for="account in pagedAccounts"
                :key="account.id"
                data-test="harvest-account-card"
                class="flex min-h-[13.75rem] flex-col rounded-2xl border border-gray-100 p-4 dark:border-dark-700"
              >
                <div class="flex items-start justify-between gap-2">
                  <div class="min-w-0">
                    <p class="font-medium text-gray-900 dark:text-white">{{ account.name || `#${account.id}` }}</p>
                    <p class="text-xs text-gray-400">
                      {{ account.status }} · {{ account.schedulable ? t('admin.harvestFlow.schedulable') : t('admin.harvestFlow.unschedulable') }}
                      <span v-if="account.skip_harvest"> · {{ t('admin.harvestFlow.skipHarvestBadge') }}</span>
                      <span v-else-if="account.in_scope"> · {{ t('admin.harvestFlow.harvestAccountBadge') }}</span>
                      <span
                        class="ml-1 whitespace-nowrap rounded-full border px-1.5 py-0.5 text-[10px] font-semibold"
                        :class="harvestAvailabilityClass(resolveHarvestAvailability(account))"
                      >{{ t(`admin.harvestFlow.availability.${resolveHarvestAvailability(account)}`) }}</span>
                    </p>
                  </div>
                  <div class="flex shrink-0 items-center gap-2">
                    <button
                      type="button"
                      class="btn btn-secondary btn-sm min-w-[5.5rem]"
                      :disabled="saving"
                      @click="toggleSkipHarvest(account)"
                    >
                      {{ isHarvestSelected(account) ? t('admin.harvestFlow.skipHarvest') : t('admin.harvestFlow.enableHarvest') }}
                    </button>
                    <span class="w-8 text-right text-xs text-gray-400">{{ account.ready_count }}/{{ account.tickets?.length ?? 0 }}</span>
                  </div>
                </div>
                <p data-test="harvest-account-hint" class="mt-1 min-h-[2.5rem] text-[11px] leading-5 text-gray-500 dark:text-gray-400">
                  <span v-if="account.skip_harvest">{{ t('admin.harvestFlow.skipHarvestHint') }}</span>
                  <span v-else-if="recoverText(account)">{{ recoverText(account) }}</span>
                </p>
                <HarvestAccountCoverage :coverage="account.coverage" :generated-at="snapshot.generated_at" />
                <div class="mt-auto grid grid-cols-2 gap-2">
                  <span
                    v-for="ticket in account.tickets"
                    :key="ticket.model"
                    data-test="harvest-ticket-chip"
                    class="flex min-h-[5.75rem] flex-col rounded-2xl px-2.5 py-1.5 text-xs"
                    :class="ticket.blocked ? 'bg-rose-50 text-rose-700 dark:bg-rose-900/30 dark:text-rose-300' : ticket.ready ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
                  >
                    <span class="font-medium">{{ ticket.model }} · {{ ticketStatusLine(account, ticket) }}</span>
                    <span class="block min-h-[1rem] text-[11px] opacity-80">{{ ticketCookieLine(ticket) }}</span>
                    <span class="block min-h-[1rem] text-[11px] opacity-80">{{ ticketProbeLine(ticket) }}</span>
                  </span>
                </div>
              </div>
            </div>
            <Pagination
              v-if="(snapshot.accounts?.length ?? 0) > 0"
              data-test="harvest-account-pagination"
              class="mt-4 rounded-xl border"
              :total="snapshot.accounts?.length ?? 0"
              :page="accountPage"
              :page-size="accountPageSize"
              @update:page="accountPage = $event"
              @update:page-size="accountPageSize = $event; accountPage = 1"
            />
          </div>

          <div class="card min-w-0 p-5 xl:col-span-3">
            <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
              <h2 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.harvestFlow.events') }}</h2>
              <div class="flex flex-wrap gap-1">
                <button
                  v-for="filter in eventFilters"
                  :key="filter"
                  type="button"
                  class="rounded-full px-2.5 py-1 text-[11px]"
                  :class="eventFilter === filter ? 'bg-primary-600 text-white' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'"
                  @click="eventFilter = filter"
                >
                  {{ tabLabel(filter) }}
                </button>
              </div>
            </div>
            <HarvestNodeRecords
              v-if="eventFilter === 'nodes'"
              :refresh-key="snapshot?.generated_at"
              :account-names="accountNames"
            />
            <div v-else-if="!snapshot.events?.length" class="text-sm text-gray-500">{{ t('admin.harvestFlow.noEvents') }}</div>
            <ol v-else class="max-h-[560px] space-y-2 overflow-auto pr-1">
              <li
                v-for="event in filteredEvents"
                :key="event.id"
                class="flex gap-3 rounded-xl border border-gray-100 p-3 dark:border-dark-700"
              >
                <span class="mt-1 h-2.5 w-2.5 flex-shrink-0 rounded-full" :class="eventDotClass(event.kind)" />
                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center justify-between gap-2">
                    <p class="text-sm font-medium text-gray-900 dark:text-white">
                      {{ kindLabel(event.kind, event.result) }}
                      <span v-if="event.model" class="ml-1 font-mono text-xs text-gray-500">{{ event.model }}</span>
                    </p>
                    <span class="text-[11px] text-gray-400">{{ formatClock(event.at) }}</span>
                  </div>
                  <p class="mt-1 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
                    <span v-if="event.account_name">{{ event.account_name }}</span>
                    <span v-if="event.node"> · {{ event.node }}</span>
                    <span v-if="event.http_status"> · HTTP {{ event.http_status }}</span>
                    <span v-if="event.standby"> · {{ t('admin.harvestFlow.standby') }}</span>
                  </p>
                  <p v-if="event.reason || event.detail || event.result" class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ eventDetail(event) }}
                  </p>
                </div>
              </li>
            </ol>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import HarvestCoverageCard from '@/components/admin/HarvestCoverageCard.vue'
import HarvestAccountCoverage from '@/components/admin/HarvestAccountCoverage.vue'
import HarvestPolicyPanel from '@/components/admin/HarvestPolicyPanel.vue'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import SmartOpsNav from '@/components/admin/operations/SmartOpsNav.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import HarvestControlsPanel from '@/components/admin/HarvestControlsPanel.vue'
import HarvestNodeRecords from '@/components/admin/HarvestNodeRecords.vue'
import { archiveExpiredCodexGateways, getCodexHarvestFlow, updateCodexSkipHarvest, type CodexHarvestFlowAccount, type CodexHarvestFlowEvent, type CodexHarvestFlowSnapshot, type CodexHarvestFlowStage, type CodexHarvestFlowTicket, type CodexHarvestGateway, type CodexHarvestGatewayTurn } from '@/api/admin/accounts'
import { harvestAvailabilityClass, harvestAvailabilityRecoverClock, resolveHarvestAvailability } from '@/utils/harvestAvailability'

const { t } = useI18n()
const snapshot = ref<CodexHarvestFlowSnapshot | null>(null)
const accountPage = ref(1)
const accountPageSize = ref(10)
const pagedAccounts = computed(() => {
  const accounts = snapshot.value?.accounts || []
  const pages = Math.max(1, Math.ceil(accounts.length / accountPageSize.value))
  const page = Math.min(Math.max(accountPage.value, 1), pages)
  return accounts.slice((page - 1) * accountPageSize.value, page * accountPageSize.value)
})
watch(() => snapshot.value?.accounts?.length ?? 0, (total) => {
  const pages = Math.max(1, Math.ceil(total / accountPageSize.value))
  if (accountPage.value > pages) accountPage.value = pages
})
const expandedGatewayHosts = ref<Record<string, boolean>>({})
const archivingGateways = ref(false)
const gatewayExpiredCount = computed(() => (snapshot.value?.gateways || []).filter(gateway => gateway.status === 'expired' || gateway.status === 'retired').length)
async function archiveExpiredGateways() {
  if (disposed || archivingGateways.value || !gatewayExpiredCount.value) return
  archivingGateways.value = true
  errorMessage.value = ''
  let archived = false
  try {
    await archiveExpiredCodexGateways()
    archived = true
  } catch (error) {
    if (disposed) return
    const err = error as { message?: string }
    errorMessage.value = err?.message || String(error)
  } finally {
    if (!disposed) archivingGateways.value = false
  }
  if (archived && !disposed) await fetchFlow()
}
const gatewayUniqueCount = computed(() => new Set((snapshot.value?.gateways || []).map(gateway => gateway.host)).size)
const gatewayGroups = computed(() => {
  const groups: { host: string; rows: CodexHarvestGateway[]; backups: CodexHarvestGateway[] }[] = []
  for (const gateway of snapshot.value?.gateways || []) {
    let group = groups.find(item => item.host === gateway.host)
    if (!group) {
      group = { host: gateway.host, rows: [], backups: [] }
      groups.push(group)
    }
    if (gateway.role === 'backup') group.backups.push(gateway)
    else group.rows.push(gateway)
  }
  for (const group of groups) {
    if (!group.rows.length && group.backups.length) group.rows.push(group.backups.shift()!)
  }
  return groups
})

function toggleGatewayBackups(host: string) {
  expandedGatewayHosts.value = { ...expandedGatewayHosts.value, [host]: !expandedGatewayHosts.value[host] }
}
const accountNames = computed(() => Object.fromEntries((snapshot.value?.accounts || []).map(account => [account.id, account.name])))
const loading = ref(false)
const refreshing = ref(false)
const autoRefresh = ref(true)
const errorMessage = ref('')
const lastUpdated = ref('')
const eventFilter = ref<'all' | 'node' | 'probe' | 'shape' | 'ticket' | 'select' | 'nodes'>('all')
const eventFilters = ['all', 'node', 'probe', 'shape', 'ticket', 'select', 'nodes'] as const
const saving = ref(false)
const harvesting = ref(false)
const nowMs = ref(Date.now())
let timer: number | undefined
let clock: number | undefined
let flowRequestID = 0
let disposed = false

const filteredEvents = computed(() => {
  const events = snapshot.value?.events || []
  if (eventFilter.value === 'all') return events
  if (eventFilter.value === 'shape') {
    return events.filter((event: CodexHarvestFlowEvent) => event.stage === 'probe' || event.kind === 'accept' || event.kind === 'borrow' || event.kind === 'reject')
  }
  return events.filter((event: CodexHarvestFlowEvent) => event.stage === eventFilter.value)
})

function tabLabel(filter: typeof eventFilters[number]) {
  if (filter === 'all') return t('admin.harvestFlow.filterAll')
  if (filter === 'nodes') return t('admin.harvestFlow.nodes.title')
  return t(`admin.harvestFlow.stages.${filter}`)
}

const stageIcons: Record<string, 'globe' | 'bolt' | 'beaker' | 'key' | 'user'> = {
  node: 'globe',
  probe: 'bolt',
  shape: 'beaker',
  ticket: 'key',
  select: 'user'
}

function stageIcon(id: string) {
  return stageIcons[id] || 'bolt'
}

function stageCardClass(status: string) {
  switch (status) {
    case 'ok':
      return 'border-emerald-200 bg-emerald-50/60 dark:border-emerald-900/40 dark:bg-emerald-950/20'
    case 'warn':
      return 'border-amber-200 bg-amber-50/70 dark:border-amber-900/40 dark:bg-amber-950/20'
    case 'fail':
      return 'border-rose-200 bg-rose-50/70 dark:border-rose-900/40 dark:bg-rose-950/20'
    default:
      return 'border-gray-100 bg-gray-50/80 dark:border-dark-700 dark:bg-dark-800/40'
  }
}

function stageIconClass(status: string) {
  switch (status) {
    case 'ok':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
    case 'warn':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
    case 'fail':
      return 'bg-rose-100 text-rose-700 dark:bg-rose-900/40 dark:text-rose-300'
    default:
      return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-300'
  }
}

function stageTextClass(status: string) {
  switch (status) {
    case 'ok':
      return 'text-emerald-600 dark:text-emerald-400'
    case 'warn':
      return 'text-amber-600 dark:text-amber-400'
    case 'fail':
      return 'text-rose-600 dark:text-rose-400'
    default:
      return 'text-gray-400'
  }
}

function eventDotClass(kind: string) {
  if (kind === 'candy_pass' || kind === 'probe_hit' || kind === 'accept' || kind === 'borrow' || kind === 'business_borrow' || kind === 'gateway_seen' || kind === 'gateway_turn' || kind === 'schedule_join' || kind === 'rotate') {
    return 'bg-emerald-500'
  }
  if (kind === 'schedule_leave' || kind === 'probe_miss') {
    return 'bg-amber-500'
  }
  return 'bg-rose-500'
}

function lookupLabel(prefix: string, value?: string) {
  if (!value) return ''
  const key = `${prefix}.${value}`
  const label = t(key)
  return label === key ? value : label
}

function statusLabel(status: string) {
  return lookupLabel('admin.harvestFlow.status', status)
}

function kindLabel(kind: string, mode?: string) {
  if (mode && mode !== 'groups' && kind === 'schedule_join') return lookupLabel('admin.harvestFlow.kinds', 'schedule_enable')
  if (mode && mode !== 'groups' && kind === 'schedule_leave') return lookupLabel('admin.harvestFlow.kinds', 'schedule_disable')
  return lookupLabel('admin.harvestFlow.kinds', kind)
}

function reasonLabel(reason?: string) {
  if (!reason) return ''
  const harvest = lookupLabel('admin.harvestFlow.reasons', reason)
  return harvest === reason ? lookupLabel('admin.astraGateway.reasons', reason) : harvest
}

function resultLabel(result?: string) {
  return lookupLabel('admin.harvestFlow.results', result)
}

function eventDetail(event: CodexHarvestFlowEvent) {
  if (event.kind === 'reject') return t('admin.harvestFlow.ticketRejected')
  if (event.kind === 'borrow' || event.kind === 'business_borrow') {
    const id = Number(/^#(\d+)$/.exec(event.detail || '')?.[1])
    const source = id && accountNames.value[id] ? `#${id} ${accountNames.value[id]}` : event.detail || '—'
    const key = event.kind === 'business_borrow' ? 'admin.harvestFlow.borrowedForRequest' : 'admin.harvestFlow.borrowedFrom'
    return t(key, { source, model: event.model || '—', gateway: event.node || '—' })
  }
  if (event.kind === 'probe_hit') {
    return event.reason === 'probe_verified' ? t('admin.harvestFlow.probeVerified') : t('admin.harvestFlow.probeIncomplete')
  }
  // Stored history may predate probe validation. Render the stable outcome,
  // not old free-text claims about ticket length or model quality.
  if (event.stage === 'probe' && ['invalid_state', 'invalid_route', 'probe_reissued', 'probe_route_changed', 'probe_inconclusive'].includes(event.result || '')) {
    return resultLabel(event.result)
  }
  if (event.stage === 'select') {
    const mode = event.result || ''
    const modeKey = mode === 'groups' ? 'modeGroups' : mode === 'model' ? 'modeModel' : mode === 'account' ? 'modeAccount' : ''
    const modeLabel = modeKey ? lookupLabel('admin.astraGateway', modeKey) : ''
    return [reasonLabel(event.reason), modeLabel].filter(Boolean).join(' · ') || event.detail || resultLabel(event.result)
  }
  return event.detail || reasonLabel(event.reason) || resultLabel(event.result)
}

function scopeLabel(mode?: string, policy?: string, groupIds?: number[]) {
  if (snapshot.value?.harvest.scope_error) return t('admin.harvestFlow.scopeUnavailable')
  if (mode === 'selected_accounts') return t('admin.harvestFlow.scopeSelectedAccounts')
  const scope = mode === 'selected'
    ? t('admin.harvestFlow.scopeSelected', { n: groupIds?.length || 0 })
    : t('admin.harvestFlow.scopeAll')
  const accountPolicy = policy === 'prioritize_schedulable'
    ? t('admin.harvestFlow.policyPrioritize')
    : t('admin.harvestFlow.policySchedulable')
  return `${scope} · ${accountPolicy}`
}

function stageDetail(stage: CodexHarvestFlowStage) {
  switch (stage.id) {
    case 'node':
      if (snapshot.value?.sidecar.mode === 'external') return t('admin.harvestFlow.externalProxyHint')
      if (snapshot.value?.sidecar.mode === 'unconfigured') return t('admin.harvestFlow.proxyUnconfigured')
      if (stage.node) return stage.node
      if (snapshot.value?.sidecar.now) return snapshot.value.sidecar.now
      if (snapshot.value?.sidecar.reachable) {
        return t('admin.harvestFlow.poolOnline', { n: snapshot.value.sidecar.all_count || 0 })
      }
      return t('admin.harvestFlow.waitingSidecar')
    case 'probe':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleProbe')
      return resultLabel(stage.detail) || stage.node || stage.detail || t('admin.harvestFlow.idleProbe')
    case 'shape':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleShape')
      if (stage.detail === 'probe_verified') return t('admin.harvestFlow.probeVerified')
      if (['probe_reissued', 'probe_route_changed', 'invalid_state', 'invalid_route', 'network_error', 'account_error', 'rate_limited'].includes(stage.detail || '')) return resultLabel(stage.detail)
      return t('admin.harvestFlow.probeIncomplete')
    case 'ticket':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleTicket')
      if (stage.status === 'fail') {
        return snapshot.value?.counts.tickets_blocked
          ? t('admin.harvestFlow.ticketsPausedCount', { n: snapshot.value.counts.tickets_blocked })
          : t('admin.harvestFlow.ticketRejected')
      }
      if (!snapshot.value?.counts.tickets_ready) return t('admin.harvestFlow.idleTicket')
      return t('admin.harvestFlow.ticketsReadyCount', { n: snapshot.value?.counts.tickets_ready ?? 0 })
    case 'select':
      if (stage.status === 'idle') return t('admin.harvestFlow.idleSelect')
      return reasonLabel(stage.detail) || resultLabel(stage.detail) || stage.detail || t('admin.harvestFlow.idleSelect')
    default:
      return stage.detail || '—'
  }
}

function formatClock(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleTimeString()
}

function recoverText(account: CodexHarvestFlowAccount) {
  const recovered = harvestAvailabilityRecoverClock(account.recover_at)
  if (!recovered) return ''
  return recovered.date
    ? t('admin.harvestFlow.availability.recoverLater', { date: recovered.date, time: recovered.clock })
    : t('admin.harvestFlow.availability.recoverToday', { time: recovered.clock })
}

function gatewayTurnName(turn: CodexHarvestGatewayTurn) {
  return snapshot.value?.accounts?.find(account => account.id === turn.account_id)?.name || ''
}

function gatewayTurnRemainText(turn: CodexHarvestGatewayTurn) {
  const left = secondsUntil(turn.until) ?? Math.max(0, turn.remaining_seconds || 0)
  if (left <= 0) return t('admin.harvestFlow.gatewayTurnDue')
  return t('admin.harvestFlow.gatewayTurnRemaining', { time: formatRemaining(left) })
}

function gatewayStatusLabel(status?: string) {
  if (status === 'retired') return t('admin.harvestFlow.gatewayStatusRetired')
  if (status === 'expired') return t('admin.harvestFlow.gatewayStatusExpired')
  return t('admin.harvestFlow.gatewayStatusActive')
}

function gatewayStatusClass(status?: string) {
  if (status === 'retired') return 'bg-rose-50 text-rose-600 dark:bg-rose-900/30 dark:text-rose-300'
  if (status === 'expired') return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-300'
  return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
}

function gatewayRemainingSeconds(gateway: CodexHarvestGateway) {
  if (gateway.status === 'expired') return 0
  return secondsUntil(gateway.expires_at) ?? Math.max(0, gateway.remaining_seconds || 0)
}

function gatewayRemainText(gateway: CodexHarvestGateway) {
  const left = gatewayRemainingSeconds(gateway)
  if (gateway.status === 'expired' || left <= 0) return t('admin.harvestFlow.gatewayStatusExpired')
  const minutes = Math.floor(left / 60)
  const seconds = left % 60
  if (minutes <= 0) return t('admin.harvestFlow.durationSeconds', { seconds })
  return t('admin.harvestFlow.gatewayCountdown', { minutes, seconds: String(seconds).padStart(2, '0') })
}

function gatewayBarWidth(gateway: CodexHarvestGateway) {
  const left = gatewayRemainingSeconds(gateway)
  const pct = Math.max(0, Math.min(100, (left / 3600) * 100))
  return `${pct}%`
}

function gatewayBarClass(gateway: CodexHarvestGateway) {
  const left = gatewayRemainingSeconds(gateway)
  if (gateway.status === 'expired' || left <= 0) return 'bg-gray-300'
  if (left >= 30 * 60) return 'bg-emerald-500'
  if (left >= 10 * 60) return 'bg-amber-500'
  return 'bg-rose-500'
}

function formatRemaining(seconds: number) {
  const total = Math.max(0, Math.floor(seconds))
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (hours > 0 && minutes > 0) return t('admin.harvestFlow.durationHoursMinutes', { hours, minutes })
  if (hours > 0) return t('admin.harvestFlow.durationHours', { hours })
  if (minutes > 0) return t('admin.harvestFlow.durationMinutes', { minutes })
  return t('admin.harvestFlow.durationSeconds', { seconds: total })
}

function secondsUntil(value?: string) {
  if (!value) return null
  const exp = Date.parse(value)
  if (Number.isNaN(exp)) return null
  return Math.max(0, Math.floor((exp - nowMs.value) / 1000))
}

function remainingSeconds(ticket: CodexHarvestFlowTicket) {
  return secondsUntil(ticket.expires_at) ?? Math.max(0, ticket.remaining_seconds || 0)
}

function ticketStatusLine(account: CodexHarvestFlowAccount, ticket: CodexHarvestFlowTicket) {
  if (ticket.blocked) {
    return account.in_scope ? t('admin.harvestFlow.blocked') : t('admin.harvestFlow.reasons.harvest_excluded')
  }
  if (!ticket.ready) return t('admin.harvestFlow.missing')
  const time = formatRemaining(remainingSeconds(ticket))
  if (ticket.standby) return t('admin.harvestFlow.remainingStandby', { time })
  const standby = secondsUntil(ticket.standby_expires_at)
  if (standby && standby > 0) {
    return t('admin.harvestFlow.remainingWithStandby', { time, standby: formatRemaining(standby) })
  }
  return t('admin.harvestFlow.remaining', { time })
}

function ticketProbeLine(ticket: CodexHarvestFlowTicket) {
  if (ticket.ready && ticket.transport) return [ticket.transport.toUpperCase(), ticket.gateway, ticket.edge_ip].filter(Boolean).join(' · ')
  const probe = ticket.probe
  if (!probe?.result && !probe?.checked_at) return '\u00a0'
  if (!ticket.ready && probe?.checked_at) {
    return t('admin.harvestFlow.lastProbe', { time: formatClock(probe.checked_at) })
  }
  if (!probe?.result) return '\u00a0'
  return probe.http_status ? `${resultLabel(probe.result)} · HTTP ${probe.http_status}` : resultLabel(probe.result)
}

function ticketCookieLine(ticket: CodexHarvestFlowTicket) {
  if (!ticket.cookie_count) return t('admin.harvestFlow.cookies.none')
  const remaining = secondsUntil(ticket.cookie_expires_at)
  if (remaining === null || remaining <= 0) {
    return t('admin.harvestFlow.cookies.expired', { count: ticket.cookie_count })
  }
  return t('admin.harvestFlow.cookies.active', { count: ticket.cookie_count, time: formatRemaining(remaining) })
}

async function fetchFlow() {
  if (disposed || saving.value) return
  const requestID = ++flowRequestID
  if (snapshot.value) {
    refreshing.value = true
  } else {
    loading.value = true
  }
  errorMessage.value = ''
  try {
    const data = await getCodexHarvestFlow()
    if (disposed || requestID !== flowRequestID) return
    snapshot.value = data
    nowMs.value = Date.now()
    lastUpdated.value = new Date().toLocaleTimeString()
  } catch (error) {
    if (disposed || requestID !== flowRequestID) return
    const err = error as { message?: string }
    errorMessage.value = err?.message || String(error)
  } finally {
    if (!disposed && requestID === flowRequestID) {
      loading.value = false
      refreshing.value = false
    }
  }
}

function isHarvestSelected(account: CodexHarvestFlowAccount) {
  return account.harvest_selected ?? !account.skip_harvest
}

async function toggleSkipHarvest(account: CodexHarvestFlowAccount) {
  if (disposed || saving.value) return
  saving.value = true
  // Reads started before this write must never replace the confirmed state.
  ++flowRequestID
  loading.value = false
  refreshing.value = false
  errorMessage.value = ''
  let saved = false
  try {
    const result = await updateCodexSkipHarvest(account.id, isHarvestSelected(account))
    if (disposed) return
    const current = snapshot.value?.accounts?.find(item => item.id === account.id)
    if (current) {
      current.skip_harvest = result.skip_harvest
      current.harvest_selected = !result.skip_harvest
    }
    saved = true
  } catch (error) {
    if (disposed) return
    const err = error as { message?: string }
    errorMessage.value = err?.message || String(error)
  } finally {
    if (!disposed) saving.value = false
  }
  if (saved && !disposed) await fetchFlow()
}

onMounted(() => {
  void fetchFlow()
  timer = window.setInterval(() => {
    if (autoRefresh.value && !loading.value && !refreshing.value && !saving.value && !harvesting.value) void fetchFlow()
  }, 5000)
  clock = window.setInterval(() => {
    nowMs.value = Date.now()
  }, 1000)
})

onBeforeUnmount(() => {
  disposed = true
  ++flowRequestID
  if (timer !== undefined) window.clearInterval(timer)
  if (clock !== undefined) window.clearInterval(clock)
})
</script>
